package contextmgr

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

type phase2PreparationManager struct {
	prepared  Preparation
	discarded bool
}

func (m *phase2PreparationManager) Prepare(context.Context, PrepareInput) (Preparation, error) {
	return m.prepared, nil
}
func (m *phase2PreparationManager) Discard(Preparation) { m.discarded = true }

type phase2Publisher struct{ committed bool }

func (p *phase2Publisher) Commit(context.Context, Preparation, TurnResult) error {
	p.committed = true
	return nil
}

func TestPhase2ContextManagerAdaptersAndHelpers(t *testing.T) {
	if _, err := (ContextManager{}).Prepare(context.Background(), PrepareInput{}); err == nil {
		t.Fatal("missing preparation manager was accepted")
	}
	if err := (ContextManager{}).Commit(context.Background(), Preparation{}, TurnResult{}); err == nil {
		t.Fatal("missing checkpoint publisher was accepted")
	}
	prepared := Preparation{Compacted: true}
	manager := &phase2PreparationManager{prepared: prepared}
	publisher := &phase2Publisher{}
	adapter := ContextManager{PreparationManager: manager, CheckpointPublisher: publisher, Enabled: true}
	got, err := adapter.Prepare(context.Background(), PrepareInput{})
	if err != nil || got.Compacted != prepared.Compacted {
		t.Fatalf("Prepare = %#v, %v", got, err)
	}
	if err := adapter.Commit(context.Background(), got, TurnResult{}); err != nil || !publisher.committed {
		t.Fatalf("Commit = %v, committed=%t", err, publisher.committed)
	}

	if !errors.Is(invalidPlan("field", "reason"), contextstate.ErrInvalidDTO) {
		t.Fatal("invalidPlan did not wrap ErrInvalidDTO")
	}
	for _, key := range []string{"", " leading", "line\nbreak"} {
		if err := validatePlanKey(key); err == nil {
			t.Fatalf("invalid key %q accepted", key)
		}
	}
	if err := validatePlanKey("plan-key"); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(contextDone(ctx), context.Canceled) || contextDone(nil) != nil || contextDone(context.Background()) != nil {
		t.Fatal("contextDone did not preserve cancellation semantics")
	}
}

func TestPhase2UntrustedSummaryValueCopiesSlices(t *testing.T) {
	summary := UntrustedSummary{value: Summary{Objective: "objective", Decisions: []string{"first"}}}
	value := summary.Value()
	value.Decisions[0] = "changed"
	if summary.value.Decisions[0] != "first" {
		t.Fatal("Value exposed the stored summary slice")
	}
}
