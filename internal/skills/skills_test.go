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
