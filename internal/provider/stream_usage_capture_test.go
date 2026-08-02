package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A trailing usage-only SSE chunk with an empty choices array (the standard
// stream_options.include_usage shape - see TestOpenAIErrorParserPassesCleanCompletions's
// "empty choices with usage" case) must still be captured, not silently
// dropped by the choices-length guard that ignores content-free chunks.
func TestChatTurnStreamCapturesTrailingUsageOnlyChunk(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"prompt_cache_hit_tokens\":80,\"prompt_cache_miss_tokens\":20}}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: true})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello" {
		t.Fatalf("content = %q", resp.Content)
	}
	want := CacheUsage{Reported: true, Style: CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80, CacheWriteTokens: 0}
	if resp.CacheUsage != want {
		t.Fatalf("CacheUsage = %+v, want %+v (trailing usage-only chunk was dropped)", resp.CacheUsage, want)
	}
}

func TestChatTurnStreamCacheUsageDisabledStaysUnreported(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"prompt_cache_hit_tokens\":80}}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: false})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CacheUsage.Reported {
		t.Fatalf("disabled capture must stay unreported, got %+v", resp.CacheUsage)
	}
}

// A usage-only stream - empty choices array, no content, no finish_reason,
// closed by [DONE] - is still a completed turn: the upstream answered with
// usage accounting, so the non-streaming fallback would re-send the whole
// prompt and bill the same turn twice. Captured usage must count as a
// completion signal.
func TestChatTurnStreamUsageOnlyChunkDoesNotResend(t *testing.T) {
	chunk := `{"choices":[],"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}}`
	srv, calls := countingSSEServer(t, []string{chunk}, true)
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: true})
	resp, err := c.ChatTurn(context.Background(), Request{
		Model: "m", Stream: true,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatTurn: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("usage-only stream caused %d upstream requests, want 1 (no re-billing fallback)", got)
	}
	want := CacheUsage{Reported: true, Style: CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80, CacheWriteTokens: 0}
	if resp.CacheUsage != want {
		t.Fatalf("CacheUsage = %+v, want %+v (usage-only chunk was dropped)", resp.CacheUsage, want)
	}
}

// newRequest must never send stream_options: v1 does no request-side
// mutation at all, streamed or not - this is a passive-parse-only feature.
// This guards against a future edit accidentally reintroducing that risk
// without a corresponding decision to accept it.
func TestChatTurnStreamRequestBodyCarriesNoStreamOptions(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		captured = string(buf[:n])
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: true})
	if _, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(captured, "stream_options") {
		t.Fatalf("request body must not carry stream_options, got %s", captured)
	}
}

// Regression: the non-tool ChatStream path (readStream) must treat a
// usage-only SSE chunk as a completion signal, exactly like the tool path
// (chatTurnStream/readTurnStream) does. Without it, an empty completion
// delivered as a trailing usage-only chunk + [DONE] triggers the
// non-streaming fallback, re-sending the whole prompt and billing the same
// turn twice.
func TestChatStreamUsageOnlyChunkDoesNotResend(t *testing.T) {
	chunk := `{"choices":[],"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}}`
	srv, calls := countingSSEServer(t, []string{chunk}, true)
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k"})
	content, err := c.ChatStream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("usage-only stream caused %d upstream requests, want 1 (no re-billing fallback)", got)
	}
	if content != "" {
		t.Fatalf("content = %q, want empty", content)
	}
}
