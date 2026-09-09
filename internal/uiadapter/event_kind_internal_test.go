package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// TestLaneLog_EmptyDetailIsNil pins the empty-note guard directly.
func TestLaneLog_EmptyDetailIsNil(t *testing.T) {
	if got := laneLog(""); got != nil {
		t.Fatalf("laneLog(\"\") = %v, want nil", got)
	}
}

// TestSubagentBeginText_FallsBackWithNoDescriptionAgentOrName pins the
// final fallback: with no TaskDescription, no Agent, and no ev.Name, the
// notice still says something rather than an empty string.
func TestSubagentBeginText_FallsBackWithNoDescriptionAgentOrName(t *testing.T) {
	got := subagentBeginText(agent.Event{})
	if got != "subagent started" {
		t.Fatalf("subagentBeginText(zero-value event) = %q, want %q", got, "subagent started")
	}
}
