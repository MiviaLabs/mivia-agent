package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatTurnCapturesDeepSeekShapeCacheUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "prompt_cache_hit_tokens": 80, "prompt_cache_miss_tokens": 20},
		})
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: true})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := CacheUsage{Reported: true, Style: CacheStyleImplicit, InputTokens: 100, CachedInputTokens: 80, CacheWriteTokens: 0}
	if resp.CacheUsage != want {
		t.Fatalf("CacheUsage = %+v, want %+v", resp.CacheUsage, want)
	}
}

func TestChatTurnCapturesOpenAIShapeCacheUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 200, "prompt_tokens_details": map[string]any{"cached_tokens": 30, "cache_write_tokens": 10}},
		})
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: true})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := CacheUsage{Reported: true, Style: CacheStyleImplicit, InputTokens: 200, CachedInputTokens: 30, CacheWriteTokens: 10}
	if resp.CacheUsage != want {
		t.Fatalf("CacheUsage = %+v, want %+v", resp.CacheUsage, want)
	}
}

// CacheMarkersEnabled flips the reported cache style to explicit: the client
// now speaks a marker-style (Anthropic cache_control) wire format, so usage
// accounting must be labeled accordingly even though no current provider sets
// the option.
func TestChatTurnCacheMarkersEnabledReportsExplicitStyle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "prompt_tokens_details": map[string]any{"cached_tokens": 80}},
		})
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: true, CacheMarkersEnabled: true})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := CacheUsage{Reported: true, Style: CacheStyleExplicit, InputTokens: 100, CachedInputTokens: 80, CacheWriteTokens: 0}
	if resp.CacheUsage != want {
		t.Fatalf("CacheUsage = %+v, want %+v", resp.CacheUsage, want)
	}
}

// Guard against a default flip: with CacheMarkersEnabled left at its zero
// value the style must stay implicit, preserving existing behavior.
func TestChatTurnCacheMarkersDefaultStaysImplicit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "prompt_cache_hit_tokens": 80},
		})
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: true})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CacheUsage.Style != CacheStyleImplicit {
		t.Fatalf("default CacheUsage.Style = %q, want %q", resp.CacheUsage.Style, CacheStyleImplicit)
	}
}

// The streaming path shares the same cacheUsage conversion point, so explicit
// style must surface there too.
func TestChatTurnStreamCacheMarkersEnabledReportsExplicitStyle(t *testing.T) {
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"prompt_tokens_details\":{\"cached_tokens\":80}}}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: true, CacheMarkersEnabled: true})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello" {
		t.Fatalf("content = %q", resp.Content)
	}
	want := CacheUsage{Reported: true, Style: CacheStyleExplicit, InputTokens: 100, CachedInputTokens: 80, CacheWriteTokens: 0}
	if resp.CacheUsage != want {
		t.Fatalf("CacheUsage = %+v, want %+v", resp.CacheUsage, want)
	}
}

func TestChatTurnCacheUsageDisabledStaysUnreported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 100, "prompt_cache_hit_tokens": 80},
		})
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: false})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CacheUsage.Reported {
		t.Fatalf("disabled capture must stay unreported even though the server sent cache fields, got %+v", resp.CacheUsage)
	}
}

func TestChatTurnNoUsageFieldStaysUnreported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}},
		})
	}))
	defer srv.Close()
	c := NewOpenAICompatWithOptions(CompatOptions{Name: "test", BaseURL: srv.URL, APIKey: "k", CacheUsageEnabled: true})
	resp, err := c.ChatTurn(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.CacheUsage.Reported {
		t.Fatalf("absent usage field must stay unreported, got %+v", resp.CacheUsage)
	}
}
