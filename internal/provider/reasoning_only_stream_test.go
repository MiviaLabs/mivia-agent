package provider

import (
	"context"
	"io"
	"sync/atomic"
	"testing"
)

// H1 regression. A reasoning-only stream - thinking deltas, then [DONE], with
// no content and no finish_reason - is a delivered answer. The no-tools
// ChatStream path (readStream) must count non-empty reasoning_content as a
// completion signal, exactly as readTurnStream's payload computation
// (reasoning.Len() > 0) does on the tools path. Before the fix, readStream set
// received only for content, finish_reason, or usage, so this stream fired the
// non-streaming fallback: a second upstream request with a fresh
// Idempotency-Key, billing the same turn twice, while the tools path did not.
func TestChatStreamReasoningOnlyStreamDoesNotResend(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"reasoning_content":"think "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"more"}}]}`,
	}
	srv, calls := countingSSEServer(t, chunks, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	content, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("reasoning-only stream caused %d upstream requests, want 1 (no re-billing fallback)", got)
	}
	// readStream does not expose reasoning; the no-tools path returns only the
	// content it accumulates, which stays empty.
	if content != "" {
		t.Fatalf("content = %q, want empty", content)
	}
}

// Truncated variant of H1: reasoning deltas then a clean EOF with no [DONE]
// and no finish_reason. readTurnStream counts reasoning in its payload, so the
// tools path treats such a fragment as received; readStream must match rather
// than re-asking non-streamed.
func TestChatStreamTruncatedReasoningOnlyStreamDoesNotResend(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"reasoning_content":"think "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"more"}}]}`,
	}
	srv, calls := countingSSEServer(t, chunks, false) // clean EOF, no [DONE]
	defer srv.Close()

	c := streamingClient(t, srv)
	content, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, io.Discard)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("truncated reasoning-only stream caused %d upstream requests, want 1 (no fallback)", got)
	}
	if content != "" {
		t.Fatalf("content = %q, want empty", content)
	}
}

// Tools-path counterpart: reasoning_details entries that carry text under an
// unknown type tag (e.g. "thinking") must still count as a delivered answer.
// readTurnStream's payload gate is reasoning.Len() > 0; applyStreamChunk only
// wrote to that builder for the two known type tags, so an unknown-type
// reasoning-only stream left the payload empty and chatTurnStream re-billed
// the same turn non-streamed (a second upstream request). Every payload-bearing
// details entry must contribute, matching the non-stream resolveReasoningContent
// which concatenates text/summary with no type gate.
func TestChatTurnStreamUnknownReasoningDetailsTypeDoesNotResend(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"reasoning_details":[{"type":"thinking","text":"think "}]}}]}`,
		`{"choices":[{"delta":{"reasoning_details":[{"type":"thinking","text":"more"}]}}]}`,
	}
	srv, calls := countingSSEServer(t, chunks, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Stream:   true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolSpec{{"type": "function"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("unknown-type reasoning_details stream caused %d upstream requests, want 1 (no re-billing fallback)", got)
	}
	if want := "think more"; resp.ReasoningContent != want {
		t.Fatalf("ReasoningContent = %q, want %q", resp.ReasoningContent, want)
	}
}

// Negative boundary of the details fix: an entry with no text and no summary
// carries no payload, so it must NOT count as received and the non-streaming
// fallback must still fire (R0-1 empty-delta semantics preserved).
func TestChatTurnStreamEmptyReasoningDetailsStillFallsBack(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text"}]}}]}`,
	}
	srv, calls := countingSSEServer(t, chunks, true)
	defer srv.Close()

	c := streamingClient(t, srv)
	resp, err := c.ChatTurn(context.Background(), Request{
		Model:    "m",
		Stream:   true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools:    []ToolSpec{{"type": "function"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("empty reasoning_details entry made %d upstream requests, want 2 (stream + fallback)", got)
	}
	if resp.Content != "fallback" {
		t.Fatalf("resp.Content = %q, want %q (fallback fired)", resp.Content, "fallback")
	}
	if resp.ReasoningContent != "" {
		t.Fatalf("ReasoningContent = %q, want empty", resp.ReasoningContent)
	}
}
