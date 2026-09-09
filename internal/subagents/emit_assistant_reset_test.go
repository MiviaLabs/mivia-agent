package subagents

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// TestEmitAssistantReset_NilOnEventIsANoop pins the nil-hook guard
// directly.
func TestEmitAssistantReset_NilOnEventIsANoop(t *testing.T) {
	emitAssistantReset(agent.Options{}, "retry") // must not panic
}
