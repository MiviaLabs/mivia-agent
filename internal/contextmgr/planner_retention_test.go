package contextmgr

import (
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func retentionMessages() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, Content: "older answer"},
	}
}

func TestPlanRejectsAnOutOfRangeRecentTail(t *testing.T) {
	messages := retentionMessages()
	budget, err := provider.EstimateRequestCost(messages, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tail := range []int{-1, maxRecentTailMessages + 1} {
		_, err := Plan(PlanInput{Messages: messages, Budget: budget + 100, Force: true, RecentTail: tail})
		if !errors.Is(err, contextstate.ErrInvalidDTO) {
			t.Fatalf("recent_tail=%d error = %v, want ErrInvalidDTO", tail, err)
		}
	}
}

func TestRetainMessagesChargesTheHoistedSchemaCost(t *testing.T) {
	// The tool schemas are priced once by Plan and handed down, so retention
	// must charge that number rather than silently costing messages only.
	messages := retentionMessages()
	input := PlanInput{Messages: messages, Budget: 1_000_000}
	free, err := retainMessages(input, messages[1], 1, 1_000_000, 0)
	if err != nil {
		t.Fatalf("retainMessages(schemaCost=0): %v", err)
	}
	// A schema charge larger than the whole budget must make the mandatory
	// selection overflow, which is the only way the charge can be observed
	// from outside.
	if _, err := retainMessages(input, messages[1], 1, 1_000_000, 2_000_000); !errors.Is(err, contextstate.ErrPromptBudgetExceeded) {
		t.Fatalf("error = %v, want ErrPromptBudgetExceeded once the schema charge exceeds the budget", err)
	}
	if len(free) == 0 {
		t.Fatal("retention kept nothing with an unconstrained budget")
	}
}
