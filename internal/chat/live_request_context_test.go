package chat

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestContextUsageFollowsTheLiveRequest is the regression for a context
// readout that only moved when a turn ended. The loop adopts its history into
// the session at turn finish, so mid-turn the session described the PREVIOUS
// turn: on the first turn it saw the system prompt alone, and a reader
// watching the context fill up saw nothing of what was filling it.
func TestContextUsageFollowsTheLiveRequest(t *testing.T) {
	s := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	s.Messages = []provider.Message{{Role: provider.RoleSystem, Content: strings.Repeat("s", 400)}}
	s.MaxContextTokens = 100_000

	before := s.ContextUsage()
	if before.Breakdown.Prose != 0 || before.Breakdown.ToolResults != 0 {
		t.Fatalf("precondition: the committed history is the system prompt alone, got %+v", before.Breakdown)
	}

	// One step of a running turn: the loop prepared a request carrying the
	// user's message and a tool result the session has not adopted.
	s.observeRequestHistory(append(s.Messages,
		provider.Message{Role: provider.RoleUser, Content: strings.Repeat("u", 2000)},
		provider.Message{Role: provider.RoleTool, ToolCallID: "tc-1", Content: strings.Repeat("t", 8000)},
	))

	during := s.ContextUsage()
	if during.UsedTokens <= before.UsedTokens {
		t.Errorf("used tokens did not move with the step: %d then %d", before.UsedTokens, during.UsedTokens)
	}
	if during.Breakdown.Prose == 0 {
		t.Error("the step's user message is not reported")
	}
	if during.Breakdown.ToolResults == 0 {
		t.Error("the step's tool result is not reported")
	}
	if during.Breakdown.System != before.Breakdown.System {
		t.Errorf("the system prompt changed with a step: %d then %d",
			before.Breakdown.System, during.Breakdown.System)
	}
	if got := during.Breakdown.Total(); got != during.UsedTokens {
		t.Errorf("breakdown sums to %d, used is %d", got, during.UsedTokens)
	}

	// A second step supersedes the first rather than accumulating.
	s.observeRequestHistory(s.Messages)
	if got := s.ContextUsage(); got.UsedTokens != before.UsedTokens {
		t.Errorf("a later step did not replace the earlier snapshot: %d, want %d",
			got.UsedTokens, before.UsedTokens)
	}

	// Once the turn is adopted the session describes its own history again.
	s.mu.Lock()
	s.adoptMessagesLocked(s.Messages)
	s.mu.Unlock()
	if got := s.ContextUsage(); got.UsedTokens != before.UsedTokens {
		t.Errorf("after the turn the session did not fall back to Messages: %d, want %d",
			got.UsedTokens, before.UsedTokens)
	}
}

// TestObserveRequestHistoryClonesItsInput: the hook runs on the loop
// goroutine with a slice the loop reuses, so retaining it would race the next
// step's preparation and silently misreport the context.
func TestObserveRequestHistoryClonesItsInput(t *testing.T) {
	s := NewSession(&config.Resolved{ProviderName: "fake", Model: "model"}, &fakeCompleter{out: "answer"})
	caller := []provider.Message{{Role: provider.RoleUser, Content: "original"}}
	s.observeRequestHistory(caller)

	caller[0].Content = "mutated by the loop"

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.liveRequest[0].Content != "original" {
		t.Errorf("the snapshot aliases the caller's slice: %q", s.liveRequest[0].Content)
	}
}
