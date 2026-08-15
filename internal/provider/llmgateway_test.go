package provider

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func TestNewLLMGatewayAppliesDefaultsAndOverrides(t *testing.T) {
	comp, err := NewLLMGateway(Options{APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.baseURL != "https://api.llmgateway.io/v1" {
		t.Fatalf("baseURL=%q", client.baseURL)
	}
	if !client.sendSessionUserKey {
		t.Fatalf("sendSessionUserKey=%v, want true", client.sendSessionUserKey)
	}
	if client.reasoning != reasoning.DialectOpenAI {
		t.Fatalf("reasoning=%q, want %q", client.reasoning, reasoning.DialectOpenAI)
	}
	if !client.replayReasoning {
		t.Fatalf("replayReasoning=%v, want true so a DeepSeek-family model behind the gateway gets its reasoning_content replayed", client.replayReasoning)
	}
	if client.rejectReasoningLessToolTurns {
		t.Fatalf("rejectReasoningLessToolTurns=%v, want false for the default openai dialect: that gate drops tool-call history and is only safe for a DeepSeek-dialect model", client.rejectReasoningLessToolTurns)
	}
	comp, err = NewLLMGateway(Options{APIKey: "fake", BaseURL: "https://example.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	client = comp.(*OpenAICompat)
	if client.baseURL != "https://example.com/v1" {
		t.Fatalf("baseURL=%q, want the override", client.baseURL)
	}
}

// TestNewLLMGatewayThinkingEffortModelGetsDeepSeekToolTurnContract pins the
// per-model gate: when NewForProvider resolves the requested model's
// reasoning_dialect to thinking_effort (DeepSeek's own wire dialect), the
// gateway client must adopt DeepSeek's documented reasoning-less-tool-turn
// reject/repair contract too - not just replay. Any other resolved dialect
// (including the provider's own "openai" default) must NOT.
func TestNewLLMGatewayThinkingEffortModelGetsDeepSeekToolTurnContract(t *testing.T) {
	comp, err := NewLLMGateway(Options{APIKey: "fake", ReasoningDialect: reasoning.DialectThinkingEffort})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.reasoning != reasoning.DialectThinkingEffort {
		t.Fatalf("reasoning=%q, want %q", client.reasoning, reasoning.DialectThinkingEffort)
	}
	if !client.replayReasoning {
		t.Fatalf("replayReasoning=%v, want true", client.replayReasoning)
	}
	if !client.rejectReasoningLessToolTurns {
		t.Fatalf("rejectReasoningLessToolTurns=%v, want true for a thinking_effort-dialect model", client.rejectReasoningLessToolTurns)
	}

	comp, err = NewLLMGateway(Options{APIKey: "fake", ReasoningDialect: reasoning.DialectOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	client = comp.(*OpenAICompat)
	if client.rejectReasoningLessToolTurns {
		t.Fatalf("rejectReasoningLessToolTurns=%v, want false for a non-thinking-effort model even when ReasoningDialect is explicitly set", client.rejectReasoningLessToolTurns)
	}
}

// TestNewLLMGatewayIgnoresCacheMarkersOption pins the deliberate
// gateway-owns-markers decision: the gateway injects its own cache_control
// markers with per-model minimums and TTL ordering mivia cannot know, so the
// factory never forwards CacheMarkersEnabled even when the caller sets it.
func TestNewLLMGatewayIgnoresCacheMarkersOption(t *testing.T) {
	comp, err := NewLLMGateway(Options{APIKey: "fake", CacheMarkersEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.cacheMarkersEnabled {
		t.Fatalf("cacheMarkersEnabled=%v, want false regardless of the option", client.cacheMarkersEnabled)
	}
}
