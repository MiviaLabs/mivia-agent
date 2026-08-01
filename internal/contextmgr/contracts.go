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
)

type PrepareInput struct {
	Messages         []provider.Message
	Budget           int
	Tools            []provider.ToolSpec
	OutputReserve    int
	Force            bool
	CurrentObjective string
	RecentTail       int
	SourceRange      contextstate.SourceRange
	Principal        contextstate.Principal
	Revision         contextstate.Revision
	Binding          contextstate.BindingRevision
	Policy           contextstate.PolicySnapshot
}

type CheckpointCandidate struct {
	ActiveContext   []byte
	SummaryMetadata []byte
	SourceEvents    []contextstate.SourceEvent
	Payloads        []contextstate.PayloadRecord
	SourceRange     contextstate.SourceRange
}

type CommitToken struct {
	Principal      contextstate.Principal
	Revision       contextstate.Revision
	Binding        contextstate.BindingRevision
	Range          contextstate.SourceRange
	IdempotencyKey string
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
	RedactionPolicy   contextstate.RedactionPolicy `json:"-"`
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
	if len(data) > contextstate.MaxSummaryMetadata {
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
	Enabled             bool
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
		Token:     CommitToken{Principal: input.Principal, Revision: input.Revision, Binding: input.Binding, Range: candidate.SourceRange, IdempotencyKey: idempotencyKey},
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
