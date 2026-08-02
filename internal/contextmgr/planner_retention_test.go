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

func TestRetainMessagesReportsACostFailureAsAnInvalidPlan(t *testing.T) {
	// Direct call: Plan prices the same tools before it gets here, so this
	// branch cannot fire through Plan. It still has to wrap rather than
	// swallow the failure if the two ever diverge.
	messages := retentionMessages()
	input := PlanInput{
		Messages: messages,
		Budget:   1000,
		// A channel has no JSON representation, so pricing the schema fails.
		Tools: []provider.ToolSpec{{"parameters": make(chan int)}},
	}

	_, err := retainMessages(input, messages[1], 1, 500)

	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("error = %v, want ErrInvalidDTO", err)
	}
}
