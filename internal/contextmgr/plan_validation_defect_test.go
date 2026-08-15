package contextmgr

import (
	"context"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func planValidationMessages() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "objective"},
	}
}

// TestPrepareRejectsOutOfRangeRecentTailBelowTrigger pins the DC-9 defect:
// the below-trigger fast path returned success without validating RecentTail,
// so the same out-of-range value was accepted below the trigger and rejected
// on the compaction path. The value must be rejected identically on both
// paths. The manager entry is StructuralPreparationManager.Prepare, the same
// call the agent loop's buildPrepareInput drives with a host-supplied
// PrepareInput.RecentTail.
func TestPrepareRejectsOutOfRangeRecentTailBelowTrigger(t *testing.T) {
	principal, binding := managerDefaultsPrincipal(t)
	messages := planValidationMessages()
	cost, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range []int{-1, maxRecentTailMessages + 1} {
		_, err := (StructuralPreparationManager{}).Prepare(context.Background(), PrepareInput{
			Messages: messages, Budget: cost * 10, RecentTail: tail,
			Principal: principal, Binding: binding, Revision: contextstate.NewRevision(1, 1, 1),
		})
		if !errors.Is(err, contextstate.ErrInvalidDTO) {
			t.Fatalf("recent_tail=%d below trigger error = %v, want ErrInvalidDTO", tail, err)
		}
	}
}

// TestPrepareRejectsOutOfRangeRecentTailOnForcedPath is the redundant-guard
// twin: the compaction path already rejected the value inside retainMessages.
// After the fix both paths reject before any compaction work, and this test
// pins that the forced path keeps rejecting.
func TestPrepareRejectsOutOfRangeRecentTailOnForcedPath(t *testing.T) {
	principal, binding := managerDefaultsPrincipal(t)
	messages := planValidationMessages()
	cost, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range []int{-1, maxRecentTailMessages + 1} {
		_, err := (StructuralPreparationManager{}).Prepare(context.Background(), PrepareInput{
			Messages: messages, Budget: cost * 10, Force: true, RecentTail: tail,
			Principal: principal, Binding: binding, Revision: contextstate.NewRevision(1, 1, 1),
		})
		if !errors.Is(err, contextstate.ErrInvalidDTO) {
			t.Fatalf("recent_tail=%d forced error = %v, want ErrInvalidDTO", tail, err)
		}
	}
}

// TestPrepareZeroRecentTailIsDefaultEight pins the DC-5 boundary: RecentTail 0
// is the default-8 marker and must keep compacting when forced, unchanged by
// the new top-of-Plan validation.
func TestPrepareZeroRecentTailIsDefaultEight(t *testing.T) {
	principal, binding := managerDefaultsPrincipal(t)
	messages := planValidationMessages()
	cost, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	prep, err := (StructuralPreparationManager{}).Prepare(context.Background(), PrepareInput{
		Messages: messages, Budget: cost * 10, Force: true, RecentTail: 0,
		Principal: principal, Binding: binding, Revision: contextstate.NewRevision(1, 1, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prep.Compacted {
		t.Fatal("zero RecentTail (default-8 marker) must still compact when forced")
	}
}

// TestPrepareAcceptsValidRecentTailBelowTrigger ensures the fast path is not
// over-rejected: an in-range RecentTail must keep succeeding below the trigger.
func TestPrepareAcceptsValidRecentTailBelowTrigger(t *testing.T) {
	principal, binding := managerDefaultsPrincipal(t)
	messages := planValidationMessages()
	cost, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	prep, err := (StructuralPreparationManager{}).Prepare(context.Background(), PrepareInput{
		Messages: messages, Budget: cost * 10, RecentTail: 2,
		Principal: principal, Binding: binding, Revision: contextstate.NewRevision(1, 1, 1),
	})
	if err != nil {
		t.Fatalf("valid RecentTail rejected on the below-trigger path: %v", err)
	}
	if prep.Compacted {
		t.Fatal("below-trigger preparation was compacted")
	}
}
