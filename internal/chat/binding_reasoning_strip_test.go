package chat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// ReasoningContent is preserved on assistant turns so the provider that
// produced it can replay its own thinking on later tool-call turns. That
// replay contract is provider-scoped: a generation that moves to a different
// provider must drop the bytes rather than ship another provider's chain of
// thought to a backend that never produced it, while a same-provider model
// change keeps them.

// reasoningHistorySession builds a session whose persisted history carries one
// assistant turn with reasoning bytes, content, and a tool call - the shape a
// thinking provider replays on the wire.
func reasoningHistorySession(t *testing.T, res *config.Resolved) *Session {
	t.Helper()
	s := NewSession(res, &fakeCompleter{out: "ok"})
	var call provider.ToolCall
	call.ID = "call_1"
	call.Type = "function"
	call.Function.Name = "read_file"
	call.Function.Arguments = `{"path":"x"}`
	s.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer", ReasoningContent: "cot", ToolCalls: []provider.ToolCall{call}},
	}
	return s
}

func TestSwitchBindingStripsReasoningOnProviderChange(t *testing.T) {
	s := reasoningHistorySession(t, &config.Resolved{
		ProviderName: "zai", Model: "thinker", SystemPrompt: "sys",
	})
	binding := ModelBinding{
		ProviderName: "openrouter",
		Model:        "different-model",
		Completer:    &fakeCompleter{out: "ok"},
		Profile:      config.ModelSpec{Name: "different-model", ContextWindowTokens: 100000},
	}
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	if got := s.CurrentSelection(); got.ProviderName != "openrouter" || got.Model != "different-model" {
		t.Fatalf("selection = %+v, want the new provider/model", got)
	}
	for _, m := range s.Messages {
		if m.ReasoningContent != "" {
			t.Fatalf("assistant reasoning survived a provider change: %q", m.ReasoningContent)
		}
	}
}

func TestSwitchBindingKeepsReasoningOnSameProviderModelChange(t *testing.T) {
	s := reasoningHistorySession(t, &config.Resolved{
		ProviderName: "zai", Model: "thinker", Models: []string{"thinker", "plain"}, SystemPrompt: "sys",
	})
	binding := ModelBinding{
		ProviderName: "zai",
		Model:        "plain",
		Completer:    &fakeCompleter{out: "ok"},
		Profile:      config.ModelSpec{Name: "plain", ContextWindowTokens: 100000},
	}
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	if got := s.CurrentSelection(); got.ProviderName != "zai" || got.Model != "plain" {
		t.Fatalf("selection = %+v, want the same provider's plain model", got)
	}
	assistant := s.Messages[len(s.Messages)-1]
	if assistant.Role != provider.RoleAssistant || assistant.ReasoningContent != "cot" {
		t.Fatalf("assistant reasoning was not preserved across a same-provider model change: %+v", assistant)
	}
}

func TestSwitchBindingStripDoesNotTouchContent(t *testing.T) {
	s := reasoningHistorySession(t, &config.Resolved{
		ProviderName: "zai", Model: "thinker", SystemPrompt: "sys",
	})
	binding := ModelBinding{
		ProviderName: "openrouter",
		Model:        "different-model",
		Completer:    &fakeCompleter{out: "ok"},
		Profile:      config.ModelSpec{Name: "different-model", ContextWindowTokens: 100000},
	}
	if err := s.SwitchBinding(binding); err != nil {
		t.Fatalf("SwitchBinding: %v", err)
	}
	assistant := s.Messages[len(s.Messages)-1]
	if assistant.Content != "answer" {
		t.Fatalf("content = %q, want the assistant turn's text intact", assistant.Content)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls = %+v, want the assistant turn's call intact", assistant.ToolCalls)
	}
	if assistant.ReasoningContent != "" {
		t.Fatalf("reasoning = %q, want stripped while the turn's content and tool calls survive", assistant.ReasoningContent)
	}
}
