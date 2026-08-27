package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestMiniMaxCompleterBasicFlow(t *testing.T) {
	const apiKey = "minimax-secret-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hello from minimax"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	comp, err := New(&config.Resolved{
		ProviderName: "minimax",
		BaseURL:      srv.URL + "/v1",
		APIKey:       apiKey,
		APIKeySet:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if comp.Name() != "minimax" {
		t.Fatalf("name=%q, want minimax", comp.Name())
	}
	req := Request{Model: "MiniMax-M3", Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	res, err := comp.Chat(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res != "hello from minimax" {
		t.Fatalf("got %q, want 'hello from minimax'", res)
	}
}

func TestMiniMaxDefaultEndpoint(t *testing.T) {
	comp, err := NewMiniMax(Options{APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	client, ok := comp.(*OpenAICompat)
	if !ok {
		t.Fatalf("type=%T, want *OpenAICompat", comp)
	}
	if client.baseURL != "https://api.minimax.io/v1" {
		t.Fatalf("baseURL=%q, want https://api.minimax.io/v1", client.baseURL)
	}
}
