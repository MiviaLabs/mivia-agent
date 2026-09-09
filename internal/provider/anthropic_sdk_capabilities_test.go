package provider

import (
	"testing"

	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
)

func TestAnthropicContextWindowReportsConfiguredCapacity(t *testing.T) {
	c := newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, false)
	if got := c.ContextWindow(); got != 0 {
		t.Fatalf("ContextWindow before NewAnthropic wiring = %d, want 0", got)
	}
	c.contextWindow = 200000
	if got := c.ContextWindow(); got != 200000 {
		t.Fatalf("ContextWindow = %d, want 200000", got)
	}
}

func TestNewAnthropicWiresContextWindowFromOptions(t *testing.T) {
	comp, err := NewAnthropic(Options{BaseURL: "https://example.invalid", APIKey: "key", ContextWindowTokens: 128000})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	ca, ok := comp.(sdkshape.ContextAccountant)
	if !ok {
		t.Fatalf("AnthropicCompleter does not implement sdkshape.ContextAccountant")
	}
	if got := ca.ContextWindow(); got != 128000 {
		t.Fatalf("ContextWindow = %d, want 128000", got)
	}
}

func TestAnthropicReasoningEffortReportsNoDefault(t *testing.T) {
	c := newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, false)
	if got := c.ReasoningEffort(); got != "" {
		t.Fatalf("ReasoningEffort = %q, want empty (no completer-level default)", got)
	}
}

func TestAnthropicEstimateTokensCountsMessagesAndTools(t *testing.T) {
	c := newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, false)
	req := sdkshape.Request{
		Messages: []sdkshape.Message{
			{Role: sdkshape.RoleUser, Content: "hello there, this is a test prompt"},
		},
		Tools: []sdkshape.ToolDefinition{
			{Name: "read_file", Description: "reads a file", Schema: []byte(`{"type":"object"}`)},
		},
	}
	got, err := c.EstimateTokens(req)
	if err != nil {
		t.Fatalf("EstimateTokens: %v", err)
	}
	if got <= 0 {
		t.Fatalf("EstimateTokens = %d, want > 0 for a non-empty request", got)
	}
}

func TestAnthropicEstimateTokensEmptyRequest(t *testing.T) {
	c := newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, false)
	got, err := c.EstimateTokens(sdkshape.Request{})
	if err != nil {
		t.Fatalf("EstimateTokens: %v", err)
	}
	// EstimateRequestCost charges a small fixed overhead even for a
	// message-less, tool-less request; it is not itself zero-cost.
	// Pin the exact baseline instead of asserting zero, so a change
	// to that overhead shows up here rather than silently drifting.
	if got != 3 {
		t.Fatalf("EstimateTokens for an empty request = %d, want 3 (fixed request overhead)", got)
	}
}

func TestAnthropicImplementsAllThreeSDKCapabilities(t *testing.T) {
	var c any = newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, false)
	if _, ok := c.(sdkshape.ContextAccountant); !ok {
		t.Error("AnthropicCompleter does not implement sdkshape.ContextAccountant")
	}
	if _, ok := c.(sdkshape.ReasoningPolicy); !ok {
		t.Error("AnthropicCompleter does not implement sdkshape.ReasoningPolicy")
	}
	if _, ok := c.(sdkshape.TokenEstimator); !ok {
		t.Error("AnthropicCompleter does not implement sdkshape.TokenEstimator")
	}
}

// TestAnthropicEstimateTokensCountsReasoningBlocks pins that reasoning blocks
// reach the estimate: sdkRequestMessagesToProvider concatenates every block's
// content into ReasoningContent, so a message carrying reasoning costs more
// than the same message without it, and two blocks cost more than one.
// Dropping the blocks would under-report the prompt and delay compaction.
func TestAnthropicEstimateTokensCountsReasoningBlocks(t *testing.T) {
	c := newAnthropicCompleter("anthropic", "https://example.invalid", "key", nil, false)
	reasoning := "the model deliberated at length about the requested change"
	request := func(blocks ...sdkshape.ReasoningBlock) sdkshape.Request {
		return sdkshape.Request{Messages: []sdkshape.Message{
			{Role: sdkshape.RoleAssistant, Content: "answer", ReasoningBlocks: blocks},
		}}
	}

	plain, err := c.EstimateTokens(request())
	if err != nil {
		t.Fatalf("EstimateTokens without reasoning: %v", err)
	}
	one, err := c.EstimateTokens(request(sdkshape.ReasoningBlock{Content: reasoning}))
	if err != nil {
		t.Fatalf("EstimateTokens with one reasoning block: %v", err)
	}
	two, err := c.EstimateTokens(request(
		sdkshape.ReasoningBlock{Content: reasoning},
		sdkshape.ReasoningBlock{Content: reasoning},
	))
	if err != nil {
		t.Fatalf("EstimateTokens with two reasoning blocks: %v", err)
	}
	if one <= plain {
		t.Fatalf("EstimateTokens with reasoning = %d, want > %d (the blocks must be counted)", one, plain)
	}
	if two <= one {
		t.Fatalf("EstimateTokens with two blocks = %d, want > %d (every block must be counted)", two, one)
	}
}
