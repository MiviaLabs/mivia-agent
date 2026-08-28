package agent

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func TestOutputReserveReturnsConfiguredLimit(t *testing.T) {
	limit := 42
	if got := outputReserve(&limit, reasoning.High); got != limit {
		t.Fatalf("output reserve = %d, want %d", got, limit)
	}
}

// TestOutputReserveFallsBackToReasoningFloorWhenUnset pins that an unset
// MaxTokens no longer fingerprints the plan's idempotency key as if it
// reserved 0: outputReserve falls back to reasoning.OutputReserveFloor(level),
// the same fallback the wire request applies (effectiveMaxTokens in
// openai_compat_request.go). This is exactly the request shape a
// subagent/task-dispatch call uses (cliorchestrate.DefaultMaxTokens = 0,
// MaxTokens left nil). Note: this value does NOT itself shrink the prompt
// budget the planner packs history against - see config.EffectiveOutputTokens
// for where that reserve is actually applied.
func TestOutputReserveFallsBackToReasoningFloorWhenUnset(t *testing.T) {
	got := outputReserve(nil, reasoning.Max)
	want := reasoning.OutputReserveFloor(reasoning.Max)
	if got != want || got == 0 {
		t.Fatalf("output reserve = %d, want %d (reasoning.OutputReserveFloor(Max), non-zero)", got, want)
	}
}

// TestOutputReserveTreatsNegativeAsUnset mirrors the original guard's
// negative-value handling: a negative caller value is not a real reserve
// request, so it falls to the same reasoning-floor default as nil rather
// than silently reserving 0.
func TestOutputReserveTreatsNegativeAsUnset(t *testing.T) {
	negative := -1
	got := outputReserve(&negative, reasoning.Off)
	want := reasoning.OutputReserveFloor(reasoning.Off)
	if got != want {
		t.Fatalf("output reserve = %d, want %d", got, want)
	}
}
