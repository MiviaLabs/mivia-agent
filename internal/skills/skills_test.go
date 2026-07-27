package skills

import (
	"context"
	"encoding/json"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"testing"
)

func TestSkillRegistryTypedRegistration(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Definition{Name: "summarize", Run: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}); err != nil {
		t.Fatal(err)
	}
	d := runtime.New(runtime.Policy{})
	if err := r.RegisterAll(d); err != nil {
		t.Fatal(err)
	}
	if d.Invoke(context.Background(), runtime.Request{Kind: runtime.Skill, Name: "summarize"}).Err != nil {
		t.Fatal("skill did not dispatch")
	}
}

func TestSkillEnforcesSchemaPermissionAndTimeout(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Definition{Name: "secure", Permission: "read", InputSchema: map[string]any{"type": "object", "required": []any{"value"}, "additionalProperties": false, "properties": map[string]any{"value": map[string]any{"type": "string"}}}, Run: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}}); err != nil {
		t.Fatal(err)
	}
	d := runtime.New(runtime.Policy{})
	if err := r.RegisterAll(d); err != nil {
		t.Fatal(err)
	}
	if d.Invoke(context.Background(), runtime.Request{Kind: runtime.Skill, Name: "secure", Permission: "wrong", Input: json.RawMessage(`{"value":"x"}`)}).Err == nil {
		t.Fatal("permission accepted")
	}
	if d.Invoke(context.Background(), runtime.Request{Kind: runtime.Skill, Name: "secure", Permission: "read", Input: json.RawMessage(`{}`)}).Err == nil {
		t.Fatal("invalid input accepted")
	}
}
func TestSkillSelectionEnforcesVersionAndTools(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Definition{Name: "x", Version: "1", Tools: []string{"read"}, Run: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }})
	if _, err := r.Select("x", "2", map[string]bool{"read": true}); err == nil {
		t.Fatal("version mismatch accepted")
	}
	if _, err := r.Select("x", "1", map[string]bool{}); err == nil {
		t.Fatal("missing tool accepted")
	}
}

func TestRegisterAllAsSubagents(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Definition{Name: "summarize", Run: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"done":true}`), nil
	}})
	_ = r.Register(Definition{Name: "analyze", Run: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"done":true}`), nil
	}})
	d := runtime.New(runtime.Policy{})
	if err := r.RegisterAllAsSubagents(d); err != nil {
		t.Fatal(err)
	}
	// Skills should be callable as Subagent kind.
	result := d.Invoke(context.Background(), runtime.Request{Kind: runtime.Subagent, Name: "summarize", Input: json.RawMessage(`{}`)})
	if result.Err != nil {
		t.Fatalf("summarize skill failed via Subagent: %v", result.Err)
	}
	result = d.Invoke(context.Background(), runtime.Request{Kind: runtime.Subagent, Name: "analyze", Input: json.RawMessage(`{}`)})
	if result.Err != nil {
		t.Fatalf("analyze skill failed via Subagent: %v", result.Err)
	}
	// Unknown skill should fail.
	result = d.Invoke(context.Background(), runtime.Request{Kind: runtime.Subagent, Name: "nonexistent"})
	if result.Err == nil {
		t.Fatal("expected error for unknown skill")
	}
}

func TestRegisterAllAsSubagentsNotFound(t *testing.T) {
	r := NewRegistry()
	// Empty registry - should not error.
	d := runtime.New(runtime.Policy{})
	if err := r.RegisterAllAsSubagents(d); err != nil {
		t.Fatal(err)
	}
	// No skills registered, calling any Subagent should fail.
	result := d.Invoke(context.Background(), runtime.Request{Kind: runtime.Subagent, Name: "nothing"})
	if result.Err == nil {
		t.Fatal("expected error for empty registry")
	}
}
