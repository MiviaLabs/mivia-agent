package agent

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestSDKCompactAfterPromptTooLongFloorsTinyBudget pins both floors in the
// prompt-too-long recovery. A model configured with a tiny MaxContextTokens
// drives the quarter-budget target to zero, and the notice's own token
// charge then drives the prune target negative. Without the floors the
// compaction would announce a 0-token target and hand
// PruneMessagesKeepTurns a non-positive budget, so the retry it exists to
// enable would be prepared from a target that means nothing.
func TestSDKCompactAfterPromptTooLongFloorsTinyBudget(t *testing.T) {
	// MaxContextTokens/4 == 0, so target hits the floor.
	const tinyBudget = 2
	if tinyBudget/4 != 0 {
		t.Fatalf("test fixture broken: %d/4 must be 0", tinyBudget)
	}
	notice := provider.Message{Role: provider.RoleUser, Content: promptTooLongCompactNotice}
	if provider.MessageTokens(notice) < 1 {
		t.Fatal("test fixture broken: the notice must cost at least one token to drive pruneTarget below 1")
	}

	var events []Event
	opts := Options{
		MaxContextTokens: tinyBudget,
		OnEvent:          func(e Event) { events = append(events, e) },
	}
	msgs := cliMessagesToSDK([]provider.Message{
		{Role: provider.RoleSystem, Content: "system prompt"},
		{Role: provider.RoleUser, Content: strings.Repeat("x", 4000)},
		{Role: provider.RoleAssistant, Content: strings.Repeat("y", 4000)},
		{Role: provider.RoleUser, Content: "the current objective"},
	})

	out := sdkCompactAfterPromptTooLong(&Loop{}, opts, msgs)

	if len(out) == 0 {
		t.Fatal("compaction returned no messages; the notice must always be appended")
	}
	// A non-positive prune target makes PruneMessagesKeepTurns return the
	// history untouched, so the compaction that exists to shrink an
	// over-long prompt would hand the retry the same prompt back.
	if len(out) >= len(msgs)+1 {
		t.Fatalf("compaction returned %d messages from %d; nothing was dropped", len(out), len(msgs))
	}
	for _, m := range out {
		if strings.Contains(m.Content, strings.Repeat("x", 4000)) {
			t.Fatal("the oldest turn survived the compaction")
		}
	}
	last := out[len(out)-1]
	if string(last.Role) != string(provider.RoleUser) || last.Content != promptTooLongCompactNotice {
		t.Fatalf("last message = %q/%q; want the user-role compaction notice", last.Role, last.Content)
	}
	if len(events) != 1 {
		t.Fatalf("emitted %d events; want exactly one EventPrune", len(events))
	}
	if events[0].Kind != EventPrune {
		t.Fatalf("event kind = %v; want %v", events[0].Kind, EventPrune)
	}
	const want = "provider rejected prompt (prompt too long); compacted to 1 tokens and retrying once"
	if events[0].Detail != want {
		t.Fatalf("event detail = %q; want %q (the floored target, not 0)", events[0].Detail, want)
	}
}
