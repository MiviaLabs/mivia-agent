package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
