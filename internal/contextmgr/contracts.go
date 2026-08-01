// Package contextmgr owns context preparation policy and provider-message
// conversion. Durable state remains in the dependency-neutral contextstate
// package.
package contextmgr

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type PrepareInput struct {
	Messages  []provider.Message
	Budget    int
	Principal contextstate.Principal
	Revision  contextstate.Revision
	Binding   contextstate.BindingRevision
	Policy    contextstate.PolicySnapshot
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
	Messages  []provider.Message
	Candidate CheckpointCandidate
	Token     CommitToken
	Compacted bool
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
	for field, value := range map[string]string{"objective": e.Objective, "state": e.State} {
		if len(value) > 2048 {
			return fmt.Errorf("%w: summary %s is too large", contextstate.ErrInvalidDTO, field)
		}
	}
	for _, values := range [][]string{e.Decisions, e.Evidence, e.ChangedSurfaces, e.OpenWork, e.Risks} {
		if len(values) > 32 {
			return fmt.Errorf("%w: summary array exceeds limit", contextstate.ErrInvalidDTO)
		}
		for _, value := range values {
			if len(value) > 2048 {
				return fmt.Errorf("%w: summary field is too large", contextstate.ErrInvalidDTO)
			}
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
	Version         uint32
	Objective       string
	State           string
	Decisions       []string
	Evidence        []string
	ChangedSurfaces []string
	OpenWork        []string
	Risks           []string
	SourceRange     contextstate.SourceRange
}

type SummaryProvider interface {
	Summarize(context.Context, SummaryRequest) (Summary, error)
}

func (r SummaryRequest) Validate() error {
	if err := r.Input.Validate(); err != nil {
		return err
	}
	if r.Budget <= 0 || r.OutputLimit <= 0 || r.OutputLimit > 2048 {
		return fmt.Errorf("%w: invalid summary budget", contextstate.ErrInvalidDTO)
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
