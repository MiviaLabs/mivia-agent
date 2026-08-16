// Package contextmgr owns context preparation policy and provider-message
// conversion. Durable state remains in the dependency-neutral contextstate
// package.
package contextmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

type PrepareInput struct {
	// Spool, when non-nil, lets compaction elision store the full body of an
	// elided tool result and name the remainder ref in the notice, so the model
	// can fetch the original bytes with read_output. Nil keeps plain
	// (non-recoverable) notices.
	Spool *remainder.Spool

	Messages         []provider.Message
	Budget           int
	Tools            []provider.ToolSpec
	OutputReserve    int
	Force            bool
	CurrentObjective string
	RecentTail       int
	// PreserveNames lists provider.Message.Name values that structural
	// retention keeps whole alongside the mandatory set. The chat layer uses
	// it for the session-owned core-memory context frame so compaction never
	// drops it.
	PreserveNames []string
	// CalibrationRatio scales token estimates in the planner for
	// heuristic drift correction. 0 means no correction.
	CalibrationRatio float64
	// ContextAccounting carries the bound provider's declared context-billing
	// profile through to Plan(), opaquely - see PlanInput.ContextAccounting.
	ContextAccounting provider.ContextAccountingProfile
	SourceRange       contextstate.SourceRange
	Principal         contextstate.Principal
	Revision          contextstate.Revision
	Binding           contextstate.BindingRevision
	WorktreeInstance  contextstate.WorktreeInstance
	Policy            contextstate.PolicySnapshot
}

type CheckpointCandidate struct {
	ActiveContext   []byte
	SummaryMetadata []byte
	SourceEvents    []contextstate.SourceEvent
	Payloads        []contextstate.PayloadRecord
	SourceRange     contextstate.SourceRange
}

type CommitToken struct {
	Principal        contextstate.Principal
	Revision         contextstate.Revision
	Binding          contextstate.BindingRevision
	WorktreeInstance contextstate.WorktreeInstance
	Range            contextstate.SourceRange
	IdempotencyKey   string
}

type Preparation struct {
	Messages      []provider.Message
	Candidate     CheckpointCandidate
	Token         CommitToken
	Compacted     bool
	BeforeTokens  int
	AfterTokens   int
	TriggerTokens int
	TargetTokens  int
	// ElidedMessages and ElidedBytes are content-free aggregates of prior-turn
	// tool-result replacements from the planner (or turn-level accumulation on
	// the agent loop). Both are zero when nothing was elided.
	ElidedMessages int
	ElidedBytes    int
	// ElidedReasoningMessages and ElidedReasoningBytes are content-free
	// aggregates of stale assistant reasoning replaced with a constant marker
	// on the planner compaction path. Both are zero when nothing was elided.
	ElidedReasoningMessages int
	ElidedReasoningBytes    int
}

// ValidateToken checks that an asynchronous preparation still belongs to the
// captured durable revision and provider/model generation before publication.
func (p Preparation) ValidateToken(revision contextstate.Revision, binding contextstate.BindingRevision) error {
	if p.Token.Revision != revision {
		return fmt.Errorf("%w: preparation revision changed", contextstate.ErrStaleRevision)
	}
	if p.Token.Binding != binding {
		return fmt.Errorf("%w: preparation binding changed", contextstate.ErrStaleBinding)
	}
	return nil
}

type PreparationManager interface {
	Prepare(context.Context, PrepareInput) (Preparation, error)
	Discard(Preparation)
}

type CheckpointPublisher interface {
	Commit(context.Context, Preparation, TurnResult) error
}

type SummaryRequest struct {
	Input             SummaryEnvelope
	Budget            int
	OutputLimit       int
	SourceRange       contextstate.SourceRange
	Provider          string
	Model             string
	EndpointAllowlist []string
	// SourceExcerpts are bounded quotes of the dropped messages, for the
	// summarize request only. The sealed envelope never carries them, so no
	// durable record or injected message contains excerpt content.
	SourceExcerpts []SourceExcerpt `json:"-"`
	// Focus is an optional caller-supplied bias string (e.g. `/compact <focus
	// instructions>`) telling the summarizer what to prioritize. It rides the
	// request only - never the sealed envelope the model echoes back or the
	// durable metadata - since it is host-side guidance, not conversation
	// content the model needs to round-trip.
	Focus           string                       `json:"-"`
	RedactionPolicy contextstate.RedactionPolicy `json:"-"`
}

// SummaryEnvelope is the only input accepted by a summary provider. The
// unexported seal prevents callers from manufacturing provider input with a
// struct literal; the host must construct and validate it through the bounded
// constructor below.
type SummaryEnvelope struct {
	Version         uint32
	Objective       string
	State           string
	Decisions       []string
	Evidence        []string
	ChangedSurfaces []string
	OpenWork        []string
	Risks           []string
	SourceRange     contextstate.SourceRange
	PolicyDigest    string
	sealed          bool
}

func NewSummaryEnvelope(version uint32, objective, state string, decisions, evidence, surfaces, openWork, risks []string, sourceRange contextstate.SourceRange, policyDigest string) (SummaryEnvelope, error) {
	envelope := SummaryEnvelope{Version: version, Objective: objective, State: state, Decisions: append([]string(nil), decisions...), Evidence: append([]string(nil), evidence...), ChangedSurfaces: append([]string(nil), surfaces...), OpenWork: append([]string(nil), openWork...), Risks: append([]string(nil), risks...), SourceRange: sourceRange, PolicyDigest: policyDigest, sealed: true}
	return envelope, envelope.Validate()
}

func (e SummaryEnvelope) Validate() error {
	if !e.sealed || e.Version == 0 {
		return fmt.Errorf("%w: summary envelope is not host-sealed", contextstate.ErrInvalidDTO)
	}
	if err := e.SourceRange.Validate(); err != nil {
		return err
	}
	if len(e.PolicyDigest) != 64 {
		return fmt.Errorf("%w: invalid summary policy digest", contextstate.ErrInvalidDTO)
	}
	if err := validateSummaryText("objective", e.Objective, true); err != nil {
		return err
	}
	if err := validateSummaryText("state", e.State, true); err != nil {
		return err
	}
	for field, values := range map[string][]string{
		"decisions": e.Decisions, "evidence": e.Evidence, "changed_surfaces": e.ChangedSurfaces,
		"open_work": e.OpenWork, "risks": e.Risks,
	} {
		if err := validateSummaryList(field, values); err != nil {
			return err
		}
	}
	data, err := contextstate.MarshalCanonical(e)
	if err != nil {
		return err
	}
	if len(data) > contextstate.EffectiveSummaryMetadataLimit() {
		return fmt.Errorf("%w: summary envelope is too large", contextstate.ErrInvalidDTO)
	}
	return nil
}

type Summary struct {
	Version         uint32                   `json:"version"`
	Objective       string                   `json:"objective"`
	State           string                   `json:"state"`
	Decisions       []string                 `json:"decisions,omitempty"`
	Evidence        []string                 `json:"evidence,omitempty"`
	ChangedSurfaces []string                 `json:"changed_surfaces,omitempty"`
	OpenWork        []string                 `json:"open_work,omitempty"`
	Risks           []string                 `json:"risks,omitempty"`
	SourceRange     contextstate.SourceRange `json:"source_range"`
}

type SummaryProvider interface {
	Summarize(context.Context, SummaryRequest) (Summary, error)
}

func (r SummaryRequest) Validate() error {
	if err := r.Input.Validate(); err != nil {
		return err
	}
	if err := validateSummaryEnvelopePolicy(r.Input, r.RedactionPolicy); err != nil {
		return err
	}
	if r.Budget <= 0 || r.OutputLimit <= 0 || r.OutputLimit > 2048 {
		return fmt.Errorf("%w: invalid summary budget", contextstate.ErrInvalidDTO)
	}
	if err := validateSummaryText("focus", r.Focus, true); err != nil {
		return err
	}
	if strings.TrimSpace(r.Provider) == "" || strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("%w: summary provider and model are required", contextstate.ErrInvalidDTO)
	}
	if len(r.Provider) > contextstate.MaxIdentifierBytes || len(r.Model) > contextstate.MaxIdentifierBytes {
		return fmt.Errorf("%w: summary provider or model is too large", contextstate.ErrInvalidDTO)
	}
	seenEndpoints := make(map[string]struct{}, len(r.EndpointAllowlist))
	for _, endpoint := range r.EndpointAllowlist {
		if strings.TrimSpace(endpoint) == "" || len(endpoint) > contextstate.MaxIdentifierBytes {
			return fmt.Errorf("%w: invalid summary endpoint allowlist", contextstate.ErrInvalidDTO)
		}
		if _, exists := seenEndpoints[endpoint]; exists {
			return fmt.Errorf("%w: duplicate summary endpoint", contextstate.ErrInvalidDTO)
		}
		seenEndpoints[endpoint] = struct{}{}
	}
	if err := r.SourceRange.Validate(); err != nil {
		return err
	}
	if r.Input.SourceRange != r.SourceRange {
		return fmt.Errorf("%w: summary source range mismatch", contextstate.ErrInvalidDTO)
	}
	if len(r.SourceExcerpts) > MaxSummaryItems {
		return fmt.Errorf("%w: too many source excerpts", contextstate.ErrInvalidDTO)
	}
	excerptTotal := 0
	for _, excerpt := range r.SourceExcerpts {
		switch excerpt.Role {
		case provider.RoleUser, provider.RoleAssistant, provider.RoleTool:
		default:
			return fmt.Errorf("%w: unknown source excerpt role %q", contextstate.ErrInvalidDTO, excerpt.Role)
		}
		if err := validateSummaryText("source excerpt", excerpt.Text, false); err != nil {
			return err
		}
		if err := validateSummaryText("source excerpt name", excerpt.Name, true); err != nil {
			return err
		}
		if len(excerpt.Name) > contextstate.MaxIdentifierBytes {
			return fmt.Errorf("%w: source excerpt name is too large", contextstate.ErrInvalidDTO)
		}
		excerptTotal += len(excerpt.Text)
	}
	if excerptTotal > MaxSummaryExcerptTotalBytes {
		return fmt.Errorf("%w: source excerpts exceed the total bound", contextstate.ErrInvalidDTO)
	}
	return nil
}

type TurnResult struct {
	User         []provider.Message
	Assistant    []provider.Message
	Tool         []provider.Message
	Active       []provider.Message
	Ordered      []provider.Message
	SourceEvents []contextstate.SourceEvent
	TurnID       uint64
	Outcome      string
	BaseDigest   string
}

type ContextManager struct {
	PreparationManager  PreparationManager
	CheckpointPublisher CheckpointPublisher
	// Summarizer is the optional LLM summarizer wiring point. Nil keeps every
	// path structural-only (today's production state: no SummaryProvider
	// exists). It is copied into chat turn configurations so both the agent
	// loop and plain chat can inject a validated summary at the request
	// boundary; the checkpoint committer reaches it through the
	// PreparationCommitter fields.
	Summarizer *Summarizer
	Enabled    bool
	// SummaryUnavailableReason names why Summarizer is nil, when it is: a
	// fixed, classified, content-free string set once at session setup (see
	// internal/cli's summaryDisabledReason). Every compaction path in the
	// session's lifetime reads it to report a real cause instead of a bare
	// "not summarized" boolean. Empty when Summarizer is configured.
	SummaryUnavailableReason string
	// UsageWriter is the optional durable usage-measurement sink. Nil keeps
	// usage events ephemeral (bus-only, today's production state everywhere
	// this isn't wired). Constructed once per session, alongside Summarizer,
	// and copied into agent.Options per turn the same way.
	UsageWriter UsageWriter
}

func (m ContextManager) Prepare(ctx context.Context, input PrepareInput) (Preparation, error) {
	if m.PreparationManager == nil {
		return Preparation{}, errors.New("context preparation is unavailable")
	}
	return m.PreparationManager.Prepare(ctx, input)
}

func (m ContextManager) Commit(ctx context.Context, preparation Preparation, result TurnResult) error {
	if m.CheckpointPublisher == nil {
		return errors.New("checkpoint publication is unavailable")
	}
	return m.CheckpointPublisher.Commit(ctx, preparation, result)
}

// CapturePreparation creates an immutable preparation token from one policy
// snapshot. It is intentionally independent of storage and provider I/O.
func CapturePreparation(input PrepareInput, candidate CheckpointCandidate, messages []provider.Message, compacted bool, idempotencyKey string) (Preparation, error) {
	if err := input.Revision.Validate(); err != nil {
		return Preparation{}, err
	}
	if err := input.Principal.Validate(); err != nil {
		return Preparation{}, err
	}
	if !input.Principal.IsBound() {
		return Preparation{}, fmt.Errorf("%w: owner capability is not bound", contextstate.ErrPrincipalMismatch)
	}
	if err := input.Binding.Validate(); err != nil {
		return Preparation{}, err
	}
	if err := input.WorktreeInstance.Validate(); err != nil {
		return Preparation{}, err
	}
	if input.Budget <= 0 {
		return Preparation{}, fmt.Errorf("%w: budget must be positive", contextstate.ErrInvalidDTO)
	}
	if len(messages) == 0 {
		return Preparation{}, fmt.Errorf("%w: prepared message set is empty", contextstate.ErrInvalidDTO)
	}
	if err := candidate.SourceRange.Validate(); err != nil {
		return Preparation{}, err
	}
	if candidate.SourceRange.Start.SessionID != candidate.SourceRange.End.SessionID {
		return Preparation{}, fmt.Errorf("%w: source range sessions differ", contextstate.ErrInvalidDTO)
	}
	return Preparation{
		Messages:  cloneMessages(messages),
		Candidate: cloneCandidate(candidate),
		Token:     CommitToken{Principal: input.Principal, Revision: input.Revision, Binding: input.Binding, WorktreeInstance: input.WorktreeInstance, Range: candidate.SourceRange, IdempotencyKey: idempotencyKey},
		Compacted: compacted,
	}, nil
}

func cloneMessages(messages []provider.Message) []provider.Message {
	out := make([]provider.Message, len(messages))
	copy(out, messages)
	for i := range out {
		out[i].ToolCalls = append([]provider.ToolCall(nil), messages[i].ToolCalls...)
	}
	return out
}

func cloneCandidate(candidate CheckpointCandidate) CheckpointCandidate {
	candidate.ActiveContext = append([]byte(nil), candidate.ActiveContext...)
	candidate.SummaryMetadata = append([]byte(nil), candidate.SummaryMetadata...)
	candidate.SourceEvents = append([]contextstate.SourceEvent(nil), candidate.SourceEvents...)
	candidate.Payloads = append([]contextstate.PayloadRecord(nil), candidate.Payloads...)
	return candidate
}
