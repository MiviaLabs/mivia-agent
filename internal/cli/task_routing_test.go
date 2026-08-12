package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
)

// A zero-agent workspace (no .mivia/agents/*.toml) is the common case, not
// an edge case - it must not break dispatch_tasks's tool schema. See
// agentNames's doc comment: a bare `nil` here marshals to JSON `null`,
// which some providers' function-schema validators (DeepSeek's, confirmed
// against the live API) reject outright, failing every tool-enabled
// request in such a workspace.
func TestTaskItemSchemaEnumNeverMarshalsToNullWithNoAgents(t *testing.T) {
	reg := agents.NewRegistry()

	schema := taskItemSchema(reg, false)

	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}

	if strings.Contains(string(encoded), `"enum":null`) {
		t.Fatalf("schema enum marshaled to JSON null, want []: %s", encoded)
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties missing or wrong type: %#v", schema["properties"])
	}
	agentProp, ok := properties["agent"].(map[string]any)
	if !ok {
		t.Fatalf("agent property missing or wrong type: %#v", properties["agent"])
	}
	enum, ok := agentProp["enum"].([]string)
	if !ok {
		t.Fatalf("agent enum missing or wrong type: %#v", agentProp["enum"])
	}
	if enum == nil {
		t.Fatal("agent enum is nil, want a non-nil (possibly empty) slice")
	}
	if len(enum) != 0 {
		t.Fatalf("expected no registered agents, got %v", enum)
	}
}

func TestAgentNamesNeverReturnsNil(t *testing.T) {
	if got := agentNames(nil); got == nil {
		t.Fatal("agentNames(nil) returned nil, want non-nil empty slice")
	}
	if got := agentNames(agents.NewRegistry()); got == nil {
		t.Fatal("agentNames(empty registry) returned nil, want non-nil empty slice")
	}
}
