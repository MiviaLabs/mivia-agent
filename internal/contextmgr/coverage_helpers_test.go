package contextmgr

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

type coveragePreparationManager struct {
	prepared  Preparation
	discarded bool
}

func (m *coveragePreparationManager) Prepare(context.Context, PrepareInput) (Preparation, error) {
	return m.prepared, nil
}
func (m *coveragePreparationManager) Discard(Preparation) { m.discarded = true }

type coveragePublisher struct{ committed bool }

func (p *coveragePublisher) Commit(context.Context, Preparation, TurnResult) error {
	p.committed = true
	return nil
}

func TestContextManagerAdaptersAndHelpers(t *testing.T) {
	if _, err := (ContextManager{}).Prepare(context.Background(), PrepareInput{}); err == nil {
		t.Fatal("missing preparation manager was accepted")
	}
	if err := (ContextManager{}).Commit(context.Background(), Preparation{}, TurnResult{}); err == nil {
		t.Fatal("missing checkpoint publisher was accepted")
	}
	prepared := Preparation{Compacted: true}
	manager := &coveragePreparationManager{prepared: prepared}
	publisher := &coveragePublisher{}
	adapter := ContextManager{PreparationManager: manager, CheckpointPublisher: publisher, Enabled: true}
	got, err := adapter.Prepare(context.Background(), PrepareInput{})
	if err != nil || !reflect.DeepEqual(got, prepared) {
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

func TestUntrustedSummaryValueCopiesSlices(t *testing.T) {
	summary := UntrustedSummary{value: Summary{Objective: "objective", Decisions: []string{"first"}}}
	value := summary.Value()
	value.Decisions[0] = "changed"
	if summary.value.Decisions[0] != "first" {
		t.Fatal("Value exposed the stored summary slice")
	}
}

func TestStructuralPreparationAndSourceProjection(t *testing.T) {
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (StructuralPreparationManager{}).Prepare(ctx, PrepareInput{Principal: principal}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preparation error = %v", err)
	}
	input := PrepareInput{
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "objective"}},
		Budget:    4096,
		Principal: principal,
		Binding:   binding,
	}
	preparation, err := (StructuralPreparationManager{}).Prepare(context.Background(), input)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if preparation.Compacted || len(preparation.Messages) != 1 || preparation.Token.IdempotencyKey == "" {
		t.Fatalf("preparation = %#v", preparation)
	}
	(StructuralPreparationManager{}).Discard(preparation)

	policy := contextstate.RedactionPolicy{Configured: true, Classifier: func([]byte) error { return nil }}
	events, payloads, err := ProjectSource(context.Background(), principal, []provider.Message{
		{Role: provider.RoleSystem, Content: "hidden"},
		{Role: provider.RoleUser, Content: "request"},
		{Role: provider.RoleTool, ToolCallID: "call", Content: "result"},
	}, 0, policy)
	if err != nil {
		t.Fatalf("ProjectSource: %v", err)
	}
	if len(events) != 2 || len(payloads) != 2 || events[0].ID.Sequence != 1 || events[1].Kind != "tool_result" {
		t.Fatalf("projected events=%#v payloads=%#v", events, payloads)
	}
}
