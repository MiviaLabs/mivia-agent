package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type generationTestTool struct{ name string }

func (t generationTestTool) Name() string               { return t.name }
func (t generationTestTool) Description() string        { return "test" }
func (t generationTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t generationTestTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}

func TestIntegrationRegistryCloneDoesNotMutateLiveGeneration(t *testing.T) {
	live := NewRegistry()
	live.Register(generationTestTool{name: "base"})
	clone := live.Clone()
	clone.Register(generationTestTool{name: "generation-only"})
	if _, ok := live.Get("generation-only"); ok {
		t.Fatal("generation-only registration mutated live registry")
	}
	if got := len(clone.List()); got != 2 {
		t.Fatalf("clone tools = %d, want 2", got)
	}
}

func TestIntegrationRegistryCloneForGenerationExcludesNamedTools(t *testing.T) {
	live := NewRegistry()
	live.Register(generationTestTool{name: "base"})
	live.Register(generationTestTool{name: "history"})

	clone := live.CloneForGenerationExcluding("history")
	if _, ok := clone.Get("base"); !ok {
		t.Fatal("generation clone dropped ordinary tool")
	}
	if _, ok := clone.Get("history"); ok {
		t.Fatal("generation clone retained explicitly excluded tool")
	}
}
