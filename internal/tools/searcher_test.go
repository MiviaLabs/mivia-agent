package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchToolRegistered(t *testing.T) {
	ws, reg := setupWS(t)
	_, ok := reg.Get("search")
	if !ok {
		t.Fatal("search tool not registered")
	}
	// Also verify new tools are registered
	_, ok = reg.Get("fetch_url")
	if !ok {
		t.Fatal("fetch_url tool not registered")
	}
	_ = ws
}

func TestSearchOpenAISchema(t *testing.T) {
	ws, reg := setupWS(t)
	tools := reg.OpenAITools()
	found := false
	for _, tool := range tools {
		fn, _ := tool["function"].(map[string]any)
		if fn["name"] == "search" {
			found = true
			params, _ := fn["parameters"].(map[string]any)
			props, _ := params["properties"].(map[string]any)
			if _, ok := props["query"]; !ok {
				t.Fatal("search schema missing query property")
			}
			if _, ok := props["search_depth"]; !ok {
				t.Fatal("search schema missing search_depth property")
			}
			if _, ok := props["topic"]; !ok {
				t.Fatal("search schema missing topic property")
			}
		}
	}
	if !found {
		t.Fatal("search tool not found in OpenAI schema")
	}
	_ = ws
}

func TestSearchWebViaRegistry(t *testing.T) {
	response := `{"results":[{"title":"Test","url":"https://example.com","content":"test content","score":0.9}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(response))
	}))
	defer srv.Close()

	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&webSearchTool{
		ws: ws, maxFetchKB: 100,
		httpClient: &http.Client{}, tavilyKey: "test-key", tavilyBaseURL: srv.URL,
	})

	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"query":"test query"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Test") {
		t.Fatalf("expected 'Test' in output, got: %q", out)
	}
}

func TestFetchURLViaRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&fetchURLTool{
		ws: ws, maxLocalBytes: 256 * 1024, maxFetchKB: 100,
		httpClient: &http.Client{}, fetchClient: nil,
		allowPrivateFetch: true,
	})

	ctx := context.Background()
	out, err := reg.Execute(ctx, "fetch_url", json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello world") {
		t.Fatalf("expected 'hello world' in output, got: %q", out)
	}
}

func TestUnwrapDDGRedirect(t *testing.T) {
	got := unwrapDDGRedirect("https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpath")
	if got != "https://example.com/path" {
		t.Fatalf("got %q", got)
	}
	if unwrapDDGRedirect("https://example.com/direct") != "https://example.com/direct" {
		t.Fatalf("direct URL changed")
	}
}

func TestTruncateUTF8(t *testing.T) {
	if got := truncateUTF8("hello", 3); got != "hel" {
		t.Fatalf("got %q", got)
	}
	if got := truncateUTF8("héllo", 3); got != "hé" {
		t.Fatalf("got %q", got)
	}
	if got := truncateUTF8("hello", 10); got != "hello" {
		t.Fatalf("got %q", got)
	}
}
