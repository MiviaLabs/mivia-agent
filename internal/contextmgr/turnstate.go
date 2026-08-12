package contextmgr

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TurnState is a bounded per-turn accumulator of host-observable facts that a
// summary request may carry. Every list is capped at MaxSummaryItems items and
// every field at MaxSummaryFieldBytes, matching the summary validators in
// summary.go, so a validated Snapshot is always envelope-valid.
//
// The zero value and a nil *TurnState are valid empty trackers: callers that
// must never fail the turn (the agent loop, the chat turn path) treat a
// rejected fact as a drop, never as an error.
type TurnState struct {
	state           string
	decisions       []string
	evidence        []string
	changedSurfaces []string
	openWork        []string
	risks           []string
}

// NewTurnState returns an empty bounded tracker.
func NewTurnState() *TurnState { return &TurnState{} }

// SetState replaces the accumulated state text (host-observed latest completed
// assistant content). An empty state is valid: nothing was observed yet.
func (t *TurnState) SetState(state string) error {
	if t == nil {
		return nil
	}
	if err := validateSummaryText("state", state, true); err != nil {
		return err
	}
	t.state = state
	return nil
}

// AddDecision appends one bounded decision. A rejected item (list full,
// oversized, duplicate, control characters, invalid UTF-8) is never stored.
func (t *TurnState) AddDecision(decision string) error {
	if t == nil {
		return nil
	}
	return t.appendItem(&t.decisions, "decisions", decision)
}

// AddEvidence appends one bounded evidence item (tool names, omitted-segment
// markers). A rejected item is never stored.
func (t *TurnState) AddEvidence(evidence string) error {
	if t == nil {
		return nil
	}
	return t.appendItem(&t.evidence, "evidence", evidence)
}

// AddChangedSurface appends one bounded changed file surface. A rejected item
// is never stored.
func (t *TurnState) AddChangedSurface(surface string) error {
	if t == nil {
		return nil
	}
	return t.appendItem(&t.changedSurfaces, "changed_surfaces", surface)
}

// AddOpenWork appends one bounded open-work item. A rejected item is never
// stored.
func (t *TurnState) AddOpenWork(work string) error {
	if t == nil {
		return nil
	}
	return t.appendItem(&t.openWork, "open_work", work)
}

// AddRisk appends one bounded risk item. A rejected item is never stored.
func (t *TurnState) AddRisk(risk string) error {
	if t == nil {
		return nil
	}
	return t.appendItem(&t.risks, "risks", risk)
}

func (t *TurnState) appendItem(list *[]string, field, value string) error {
	if len(*list) >= MaxSummaryItems {
		return fmt.Errorf("%w: summary %s has too many items", contextstate.ErrInvalidDTO, field)
	}
	if err := validateSummaryText(field, value, false); err != nil {
		return err
	}
	for _, existing := range *list {
		if existing == value {
			return fmt.Errorf("%w: summary %s contains duplicate items", contextstate.ErrInvalidDTO, field)
		}
	}
	*list = append(*list, value)
	return nil
}

// TurnStateSnapshot is a validated defensive copy of a TurnState.
type TurnStateSnapshot struct {
	State           string
	Decisions       []string
	Evidence        []string
	ChangedSurfaces []string
	OpenWork        []string
	Risks           []string
}

// Snapshot validates the accumulator against the summary validators and
// returns a defensive copy. Mutating the returned slices never changes the
// tracker. A nil tracker snapshots to an empty valid snapshot.
func (t *TurnState) Snapshot() (TurnStateSnapshot, error) {
	if t == nil {
		return TurnStateSnapshot{}, nil
	}
	if err := validateSummaryText("state", t.state, true); err != nil {
		return TurnStateSnapshot{}, err
	}
	for field, values := range map[string][]string{
		"decisions": t.decisions, "evidence": t.evidence, "changed_surfaces": t.changedSurfaces,
		"open_work": t.openWork, "risks": t.risks,
	} {
		if err := validateSummaryList(field, values); err != nil {
			return TurnStateSnapshot{}, err
		}
	}
	return TurnStateSnapshot{
		State:           t.state,
		Decisions:       append([]string(nil), t.decisions...),
		Evidence:        append([]string(nil), t.evidence...),
		ChangedSurfaces: append([]string(nil), t.changedSurfaces...),
		OpenWork:        append([]string(nil), t.openWork...),
		Risks:           append([]string(nil), t.risks...),
	}, nil
}
