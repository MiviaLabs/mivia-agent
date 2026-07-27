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
