package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestEphemeralToolUsesSafeRuntimePreview(t *testing.T) {
	const secret = "resource text must not reach audit preview"
	registry := tools.NewRegistry()
	registry.Register(tools.NewSkillResourceTool(func(context.Context, string) (string, string, error) {
		return secret, "skill resource loaded: template", nil
	}, "test-activation", 4096))
	var events []Event
	dispatcher, err := NewToolDispatcher(registry, Policy{Sink: func(event Event) { events = append(events, event) }})
	if err != nil {
		t.Fatal(err)
	}
	result := dispatcher.Invoke(context.Background(), Request{ID: "resource", Kind: Tool, Name: tools.SkillResourceToolName, Input: json.RawMessage(`{"id":"template"}`)})
	if result.Err != nil || !strings.Contains(string(result.Output), secret) {
		t.Fatalf("result=%s err=%v", result.Output, result.Err)
	}
	if strings.Contains(result.Metadata.OutputPreview, secret) || result.Metadata.OutputPreview != "skill resource loaded: template" {
		t.Fatalf("result metadata leaked resource: %#v", result.Metadata)
	}
	if len(events) != 2 || strings.Contains(events[1].Metadata.OutputPreview, secret) || events[1].Metadata.OutputPreview != "skill resource loaded: template" {
		t.Fatalf("sink events leaked resource: %#v", events)
	}
}
