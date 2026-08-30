package cliagents

import (
	"math"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

// TestAgentWallClockSaturatesInsteadOfOverflow pins saturation on an absurd
// timeout_seconds in a workspace agent definition. A bare multiply by
// time.Second overflows to a negative Duration, and WithWallClock would then
// arm an already-expired context for every run of that agent.
func TestAgentWallClockSaturatesInsteadOfOverflow(t *testing.T) {
	huge := int(math.MaxInt64)
	definition := agents.ResolvedAgent{TimeoutSeconds: &huge}
	var b AgentBinding
	b.resolveCeilings(definition, SessionDispatcherOpts{}, 0)
	if b.wallClock <= 0 {
		t.Fatalf("wallClock overflowed to %v; want a positive saturated ceiling", b.wallClock)
	}
}
