package cli

// Schema precedence is task > skill > agent > none, and every schema that wins
// is admitted before a task costs anything to spawn.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
)

func schemaFixtureRegistry(t *testing.T, def skills.Definition) *skills.Registry {
	t.Helper()
	reg := skills.NewRegistry()
	if err := reg.Register(def); err != nil {
		t.Fatal(err)
	}
	return reg
}

func objectSchema(property string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{property: map[string]any{"type": "string"}},
		"required":   []any{property},
	}
}

func TestResolveTaskSchemasFollowsPrecedence(t *testing.T) {
	skillReg := schemaFixtureRegistry(t, skills.Definition{
		Name:         "review",
		InputSchema:  objectSchema("skill_in"),
		OutputSchema: objectSchema("skill_out"),
	})
	route := taskRoute{
		skill: "review",
		agent: agents.ResolvedAgent{
			InputSchema:  objectSchema("agent_in"),
			OutputSchema: objectSchema("agent_out"),
		},
	}

	out, in, err := resolveTaskSchemas(objectSchema("task_out"), objectSchema("task_in"), route, skillReg)
	if err != nil {
		t.Fatal(err)
	}
	if !hasProperty(out, "task_out") || !hasProperty(in, "task_in") {
		t.Fatalf("task schemas did not win: out=%v in=%v", out, in)
	}

	out, in, err = resolveTaskSchemas(nil, nil, route, skillReg)
	if err != nil {
		t.Fatal(err)
	}
	if !hasProperty(out, "skill_out") || !hasProperty(in, "skill_in") {
		t.Fatalf("skill schemas did not win over the agent: out=%v in=%v", out, in)
	}

	// No skill schemas: the agent's own are used.
	bare := schemaFixtureRegistry(t, skills.Definition{Name: "review"})
	out, in, err = resolveTaskSchemas(nil, nil, route, bare)
	if err != nil {
		t.Fatal(err)
	}
	if !hasProperty(out, "agent_out") || !hasProperty(in, "agent_in") {
		t.Fatalf("agent schemas were not used: out=%v in=%v", out, in)
	}

	// Nothing anywhere resolves to no schema at all.
	out, in, err = resolveTaskSchemas(nil, nil, taskRoute{}, nil)
	if err != nil || out != nil || in != nil {
		t.Fatalf("empty route = %v, %v, %v", out, in, err)
	}
}

func TestResolveTaskSchemasRefusesInadmissibleSchemas(t *testing.T) {
	remote := map[string]any{"$ref": "https://example.com/s.json"}
	if _, _, err := resolveTaskSchemas(remote, nil, taskRoute{}, nil); err == nil ||
		!strings.Contains(err.Error(), "output_schema") {
		t.Fatalf("remote output schema = %v, want an output_schema refusal", err)
	}
	if _, _, err := resolveTaskSchemas(nil, remote, taskRoute{}, nil); err == nil ||
		!strings.Contains(err.Error(), "input_schema") {
		t.Fatalf("remote input schema = %v, want an input_schema refusal", err)
	}
}

func TestValidateTaskInput(t *testing.T) {
	stringSchema := map[string]any{"type": "string", "minLength": 3}
	if err := validateTaskInput(stringSchema, json.RawMessage(`"review this"`)); err != nil {
		t.Fatalf("a conforming prompt was refused: %v", err)
	}
	if err := validateTaskInput(stringSchema, json.RawMessage(`"no"`)); err == nil {
		t.Fatal("a too-short prompt was accepted")
	}

	object := objectSchema("goal")
	if err := validateTaskInput(object, json.RawMessage(`{"goal":"ship"}`)); err != nil {
		t.Fatalf("a conforming object was refused: %v", err)
	}
	if err := validateTaskInput(object, json.RawMessage(`{}`)); err == nil {
		t.Fatal("an object missing a required field was accepted")
	}
	// A prompt string against an object schema is validated as the string it is.
	if err := validateTaskInput(object, json.RawMessage(`"just a prompt"`)); err == nil {
		t.Fatal("a bare prompt satisfied an object schema")
	}
	if err := validateTaskInput(object, json.RawMessage(`{`)); err == nil ||
		!strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("malformed input = %v, want a JSON refusal", err)
	}
	if err := validateTaskInput(map[string]any{"$ref": "https://example.com/s.json"}, json.RawMessage(`"x"`)); err == nil {
		t.Fatal("an inadmissible schema was compiled")
	}
}

func hasProperty(schema map[string]any, name string) bool {
	props, _ := schema["properties"].(map[string]any)
	_, ok := props[name]
	return ok
}
