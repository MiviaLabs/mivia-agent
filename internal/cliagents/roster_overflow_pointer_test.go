package cliagents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

// The roster's overflow line is an instruction the model follows, and it
// pointed at "the dispatch_tasks agent enum" - a schema element that no longer
// exists (see cliorchestrate.taskItemSchema). A prompt naming a deleted
// element sends the model looking for a roster it will not find.
func TestRosterOverflowPointsAtTheFieldDescription(t *testing.T) {
	reg := agents.NewRegistry()
	for i := 0; i < SubagentRosterMaxLines+3; i++ {
		if err := reg.Publish(agents.ResolvedAgent{
			Name:        "agent-" + string(rune('a'+i)),
			Description: "does things",
		}); err != nil {
			t.Fatal(err)
		}
	}

	section := SubagentRosterSection(reg)
	if !strings.Contains(section, "more (") {
		t.Fatalf("fixture did not overflow the roster cap: %q", section)
	}
	if strings.Contains(section, "enum") {
		t.Errorf("the roster tail points at the agent enum, which no longer exists: %q", section)
	}
	if !strings.Contains(section, "agent field description") {
		t.Errorf("the roster tail must point at where the full roster now lives: %q", section)
	}
}
