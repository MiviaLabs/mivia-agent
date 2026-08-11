package provider

import (
	"context"
	"sync/atomic"
	"testing"
)

// TestChatTurnStreamDecodesReasoning verifies stream decoding of reasoning
// deltas on the ChatTurn stream path (readTurnStream).
//
// The Delta struct now carries a "reasoning" (string) wire field and a
// "reasoning_details" (array) wire field, and both stream shapes accumulate
// into Response.ReasoningContent. Each stream ends with a finish_reason chunk
// so the turn counts as received and the non-streaming fallback never fires -
// the assertions cover the reasoning accumulation itself, and the upstream
// request count stays at 1.
func TestChatTurnStreamDecodesReasoning(t *testing.T) {
	t.Run("string reasoning field accumulates", func(t *testing.T) {
		chunks := []string{
			`{"choices":[{"delta":{"reasoning":"Let me think"}}]}`,
			`{"choices":[{"delta":{"reasoning":" step by step."}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		}
		srv, calls := countingSSEServer(t, chunks, true)
		defer srv.Close()

		c := streamingClient(t, srv)
		resp, err := c.ChatTurn(context.Background(), Request{
			Model:    "m",
			Stream:   true,
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("ChatTurn: %v", err)
		}
		if got := atomic.LoadInt32(calls); got != 1 {
			t.Fatalf("made %d upstream requests, want 1", got)
		}
		want := "Let me think step by step."
		if resp.ReasoningContent != want {
			t.Fatalf("ReasoningContent = %q, want %q", resp.ReasoningContent, want)
		}
	})

	t.Run("reasoning_details array accumulates text and summary", func(t *testing.T) {
		chunks := []string{
			`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"First, check "}]}}]}`,
			`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"the invariants."}]}}]}`,
			`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":"Final answer: 42"}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		}
		srv, calls := countingSSEServer(t, chunks, true)
		defer srv.Close()

		c := streamingClient(t, srv)
		resp, err := c.ChatTurn(context.Background(), Request{
			Model:    "m",
			Stream:   true,
			Messages: []Message{{Role: RoleUser, Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("ChatTurn: %v", err)
		}
		if got := atomic.LoadInt32(calls); got != 1 {
			t.Fatalf("made %d upstream requests, want 1", got)
		}
		// Both reasoning.text and reasoning.summary entries contribute their
		// payload, concatenated in stream order.
		want := "First, check the invariants.Final answer: 42"
		if resp.ReasoningContent != want {
			t.Fatalf("ReasoningContent = %q, want %q", resp.ReasoningContent, want)
		}
	})
}
