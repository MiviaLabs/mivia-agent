package subagents

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func TestParentMessageBeforeStepFramesSteers(t *testing.T) {
	drain := func() []runtime.ParentMessage {
		return []runtime.ParentMessage{
			{Kind: "steer", Body: "focus on coordinator"},
			{Kind: "answer", Body: "dangling answer becomes steer"},
		}
	}
	fn := parentMessageBeforeStep(drain)
	msgs := fn()
	if len(msgs) != 1 {
		t.Fatalf("want 1 framed message, got %d", len(msgs))
	}
	if msgs[0].Role != provider.RoleUser {
		t.Fatalf("role = %s", msgs[0].Role)
	}
	if !strings.Contains(msgs[0].Content, "<parent-message>") {
		t.Fatalf("missing frame: %s", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "focus on coordinator") {
		t.Fatalf("missing body: %s", msgs[0].Content)
	}
	// empty drain
	if parentMessageBeforeStep(func() []runtime.ParentMessage { return nil })() != nil {
		t.Fatal("empty")
	}
}
