package agent

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func TestOutputReserveReturnsConfiguredLimit(t *testing.T) {
	limit := 42
	if got := outputReserve(&limit, reasoning.High); got != limit {
		t.Fatalf("output reserve = %d, want %d", got, limit)
	}
}

// TestOutputReserveFallsBackToReasoningFloorWhenUnset pins the fix for a
// planner/wire mismatch: an unset MaxTokens used to reserve 0 context-window
// room for the completion (buildPrepareInput's input.OutputReserve), while
// the wire request separately asks for up to
// provider.ReasoningOutputReserve(level) tokens (effectiveMaxTokens in
// openai_compat_request.go) - the planner could pack history right up to the
// full budget, then the wire request's declared max_tokens pushes
// prompt_tokens+max_tokens past the model's real context window. This is
// exactly the request shape a subagent/task-dispatch call uses
// (cliorchestrate.DefaultMaxTokens = 0, MaxTokens left nil).
func TestOutputReserveFallsBackToReasoningFloorWhenUnset(t *testing.T) {
	got := outputReserve(nil, reasoning.Max)
	want := provider.ReasoningOutputReserve(reasoning.Max)
	if got != want || got == 0 {
		t.Fatalf("output reserve = %d, want %d (provider.ReasoningOutputReserve(Max), non-zero)", got, want)
	}
}

// TestOutputReserveTreatsNegativeAsUnset mirrors the original guard's
// negative-value handling: a negative caller value is not a real reserve
// request, so it falls to the same reasoning-floor default as nil rather
// than silently reserving 0.
func TestOutputReserveTreatsNegativeAsUnset(t *testing.T) {
	negative := -1
	got := outputReserve(&negative, reasoning.Off)
	want := provider.ReasoningOutputReserve(reasoning.Off)
	if got != want {
		t.Fatalf("output reserve = %d, want %d", got, want)
	}
}
