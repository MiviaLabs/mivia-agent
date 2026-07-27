package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tavilySearchServer creates an httptest server that simulates Tavily Search API.
func tavilySearchServer(t *testing.T, statusCode int, response string, validateBody func(*testing.T, map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("expected Authorization Bearer test-key, got %q", auth)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("expected Content-Type application/json, got %q", ct)
		}
		if validateBody != nil {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			validateBody(t, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		fmt.Fprint(w, response)
	}))
}

func TestTavilySearchBasic(t *testing.T) {
	srv := tavilySearchServer(t, 200, `{"results":[{"title":"T","url":"https://x.com","content":"c","score":0.5}]}`, nil)
	defer srv.Close()

	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&webSearchTool{
		ws: ws, maxFetchKB: 100, httpClient: &http.Client{},
		tavilyKey: "test-key", tavilyBaseURL: srv.URL,
	})
	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"query":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Tavily search results") {
		t.Fatalf("expected search results, got: %q", out)
	}
}

func TestTavilySearchWithAnswer(t *testing.T) {
	srv := tavilySearchServer(t, 200,
		`{"results":[{"title":"R","url":"https://x.com","content":"c","score":0.5}],"answer":"AI answer here"}`,
		func(t *testing.T, body map[string]any) {
			if body["include_answer"] != "basic" {
				t.Fatalf("expected include_answer=basic, got %v", body["include_answer"])
			}
		})
	defer srv.Close()

	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&webSearchTool{
		ws: ws, maxFetchKB: 100, httpClient: &http.Client{},
		tavilyKey: "test-key", tavilyBaseURL: srv.URL,
	})
	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"query":"test","include_answer":"basic"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "AI answer here") {
		t.Fatalf("expected answer in output, got: %q", out)
	}
}

func TestTavilySearchAllParams(t *testing.T) {
	srv := tavilySearchServer(t, 200, `{"results":[{"title":"T","url":"https://x.com","content":"c","score":0.5}]}`,
		func(t *testing.T, body map[string]any) {
			if body["topic"] != "news" {
				t.Fatalf("expected topic=news, got %v", body["topic"])
			}
			if body["search_depth"] != "advanced" {
				t.Fatalf("expected search_depth=advanced, got %v", body["search_depth"])
			}
		})
	defer srv.Close()

	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&webSearchTool{
		ws: ws, maxFetchKB: 100, httpClient: &http.Client{},
		tavilyKey: "test-key", tavilyBaseURL: srv.URL,
	})
	ctx := context.Background()
	_, err := reg.Execute(ctx, "search", json.RawMessage(`{"query":"test","search_depth":"advanced","topic":"news"}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestTavilySearchNoKey(t *testing.T) {
	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&webSearchTool{
		ws: ws, maxFetchKB: 100, httpClient: &http.Client{},
		tavilyKey: "", webEngines: []webEngine{},
	})
	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"query":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no web results found") {
		t.Fatalf("expected fallback message, got: %q", out)
	}
}

func TestExtractSingle(t *testing.T) {
	srv := tavilySearchServer(t, 200, `{"results":[{"url":"https://x.com","content":"extracted content","raw_content":""}]}`, nil)
	defer srv.Close()

	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&extractTool{
		tavilyKey: "test-key", tavilyBaseURL: srv.URL,
		httpClient: &http.Client{},
	})
	_ = ws
	ctx := context.Background()
	out, err := reg.Execute(ctx, "extract", json.RawMessage(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "extracted content") {
		t.Fatalf("expected content in output, got: %q", out)
	}
}

func TestExtractNoKey(t *testing.T) {
	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&extractTool{tavilyKey: "", httpClient: &http.Client{}})
	_ = ws
	ctx := context.Background()
	_, err := reg.Execute(ctx, "extract", json.RawMessage(`{"url":"https://example.com"}`))
	if err == nil {
		t.Fatal("expected error when no Tavily key")
	}
	if !strings.Contains(err.Error(), "API_KEY") {
		t.Fatalf("error should mention API key: %v", err)
	}
}

func TestExtractWithQuery(t *testing.T) {
	srv := tavilySearchServer(t, 200, `{"results":[{"url":"https://x.com","content":"reranked content","raw_content":""}]}`,
		func(t *testing.T, body map[string]any) {
			if body["query"] != "find main points" {
				t.Fatalf("expected query='find main points', got %v", body["query"])
			}
		})
	defer srv.Close()

	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&extractTool{
		tavilyKey: "test-key", tavilyBaseURL: srv.URL,
		httpClient: &http.Client{},
	})
	_ = ws
	ctx := context.Background()
	out, err := reg.Execute(ctx, "extract", json.RawMessage(`{"url":"https://example.com","query":"find main points"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "reranked content") {
		t.Fatalf("expected content in output, got: %q", out)
	}
}
