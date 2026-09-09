package cliorchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

// The task "agent" field publishes a roster in its DESCRIPTION, never as a
// schema enum.
//
// An enum is enforced by the provider's validator and by the SDK's compiled
// schema, both of which run BEFORE the tool. A name the roster does not carry
// therefore died as a pre-execution tool failure counted toward the loop's
// failure-spiral bound, and the only thing the model was told is where the
// failure was: "[tool-error] enum mismatch at /tasks/0/agent" - no roster, no
// spelling to correct toward. Three of those stop the turn.
//
// Without the enum the same call reaches ResolveTaskRoute, whose error names
// the mistake AND lists every valid answer, so the model can fix the call on
// its next turn. The roster stays in the field description either way, which
// is what the model reads when composing the call in the first place.

// TestAgentFieldPublishesNoEnum pins the schema shape for both the populated
// and the empty registry.
func TestAgentFieldPublishesNoEnum(t *testing.T) {
	for name, tool := range map[string]*dispatchTasksTool{
		"populated": routingTools(t),
		"empty":     {agentReg: agents.NewRegistry()},
		"nil":       {},
	} {
		t.Run(name, func(t *testing.T) {
			items := tool.Parameters()["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
			agent := items["properties"].(map[string]any)["agent"].(map[string]any)
			if enum, found := agent["enum"]; found {
				t.Fatalf("agent enum = %#v; an enum rejects an unlisted name before the "+
					"tool runs, so the model never learns which names are valid", enum)
			}
		})
	}
}

// TestAgentRosterStaysInTheDescription is the other half: dropping the enum
// must not drop the roster. The description is where the model reads the
// available agents when it composes the call.
func TestAgentRosterStaysInTheDescription(t *testing.T) {
	items := routingTools(t).Parameters()["properties"].(map[string]any)["tasks"].(map[string]any)["items"].(map[string]any)
	description := items["properties"].(map[string]any)["agent"].(map[string]any)["description"].(string)
	for _, want := range []string{"researcher: Research evidence", "writer: Write reports"} {
		if !strings.Contains(description, want) {
			t.Errorf("agent description = %q, want it to carry %q", description, want)
		}
	}
}

// TestUnlistedAgentErrorNamesTheRoster is the payoff: the rejection the model
// now receives is the tool's own, which lists what it could have said.
func TestUnlistedAgentErrorNamesTheRoster(t *testing.T) {
	dispatch := routingTools(t)
	args := `{"tasks":[{"id":"x","agent":"code-reviewer","prompt":"work"}]}`
	_, err := dispatch.Execute(context.Background(), json.RawMessage(args))
	if err == nil {
		t.Fatal("an unlisted agent was accepted")
	}
	if !strings.Contains(err.Error(), "unknown agent") || !strings.Contains(err.Error(), "code-reviewer") {
		t.Fatalf("error = %v, want the unknown agent named", err)
	}
	for _, want := range []string{"researcher", "writer"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want the available roster listed (missing %q)", err, want)
		}
	}
}
