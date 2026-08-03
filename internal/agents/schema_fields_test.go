package agents

// Agent-declared schemas are copied, never shared, and a value that cannot be
// copied yields no schema rather than a half-copied one.

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestCloneAnyMapCopiesOrGivesUp(t *testing.T) {
	if got := cloneAnyMap(nil); got != nil {
		t.Fatalf("cloneAnyMap(nil) = %v, want nil", got)
	}
	if got := cloneAnyMap(map[string]any{"bad": make(chan int)}); got != nil {
		t.Fatalf("an unmarshalable map cloned to %v, want nil", got)
	}
	source := map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string"}}}
	clone := cloneAnyMap(source)
	if clone["type"] != "object" {
		t.Fatalf("clone = %v", clone)
	}
	clone["type"] = "mutated"
	if source["type"] != "object" {
		t.Fatal("cloneAnyMap returned a shallow alias")
	}
}

func TestInheritFieldsAdoptsDeclaredSchemas(t *testing.T) {
	out := map[string]any{"type": "object"}
	in := map[string]any{"type": "string"}
	fields := inheritFields(config.AgentFileSpec{OutputSchema: &out, InputSchema: &in}, nil, ResolveOptions{})

	if fields.outputSchema["type"] != "object" || fields.inputSchema["type"] != "string" {
		t.Fatalf("schemas not adopted: out=%v in=%v", fields.outputSchema, fields.inputSchema)
	}
	fields.outputSchema["type"] = "mutated"
	if out["type"] != "object" {
		t.Fatal("the declared output schema was aliased rather than copied")
	}

	// An agent that declares nothing inherits its parent's schemas.
	parent := &ResolvedAgent{OutputSchema: out, InputSchema: in}
	inherited := inheritFields(config.AgentFileSpec{}, parent, ResolveOptions{})
	if inherited.outputSchema["type"] != "object" || inherited.inputSchema["type"] != "string" {
		t.Fatalf("parent schemas not inherited: %+v", inherited)
	}
}
