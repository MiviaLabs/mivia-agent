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
