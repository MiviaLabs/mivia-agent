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

// tavilyTestServer creates an httptest server that simulates Tavily API responses.
// It validates the Authorization header and returns the provided response JSON.
func tavilyTestServer(t *testing.T, endpoint string, statusCode int, responseBody string, validateBody func(*testing.T, map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("expected Authorization: Bearer test-key, got %q", auth)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("expected Content-Type: application/json, got %q", ct)
		}
		if !strings.HasSuffix(r.URL.Path, endpoint) {
			t.Fatalf("expected path ending in %q, got %q", endpoint, r.URL.Path)
		}
		if validateBody != nil {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			validateBody(t, body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		fmt.Fprint(w, responseBody)
	}))
}

// tavilyTestReg creates a fresh registry with a single search tool configured
// for Tavily tests. The search tool points at the given httptest server URL
// and has no fallback web engines (Tavily errors propagate as-is).
func tavilyTestReg(t *testing.T, srvURL, tavilyKey string) *Registry {
	t.Helper()
	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&searchTool{
		ws: ws, maxLocalBytes: 256 * 1024, maxFetchKB: 100,
		httpClient: &http.Client{}, tavilyKey: tavilyKey, tavilyBaseURL: srvURL,
		webEngines: []webEngine{}, // empty: no fallback to free engines in tests
	})
	return reg
}

func TestTavilySearchBasic(t *testing.T) {
	response := `{"results":[
		{"title":"Example Title","url":"https://example.com","content":"Example content description","score":0.95},
		{"title":"Second Result","url":"https://test.org","content":"Second result description","score":0.85}]}`
	srv := tavilyTestServer(t, "/search", 200, response, nil)
	defer srv.Close()

	reg := tavilyTestReg(t, srv.URL, "test-key")
	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"web","query":"test query","search_depth":"basic"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Example Title") {
		t.Fatalf("expected 'Example Title' in output, got: %q", out)
	}
	if !strings.Contains(out, "https://example.com") {
		t.Fatalf("expected URL in output, got: %q", out)
	}
}

func TestTavilySearchWithAnswer(t *testing.T) {
	response := `{"results":[{"title":"Result","url":"https://example.com","content":"Content here","score":0.9}],"answer":"This is the AI-generated answer to the query."}`
	srv := tavilyTestServer(t, "/search", 200, response, func(t *testing.T, body map[string]any) {
		if body["include_answer"] != "basic" {
			t.Fatalf("expected include_answer='basic', got %v", body["include_answer"])
		}
	})
	defer srv.Close()

	reg := tavilyTestReg(t, srv.URL, "test-key")
	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"web","query":"test","include_answer":"basic"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "AI-generated answer") {
		t.Fatalf("expected answer in output, got: %q", out)
	}
}

func TestTavilySearchAllParams(t *testing.T) {
	response := `{"results":[{"title":"T","url":"https://x.com","content":"c","score":0.5}]}`
	srv := tavilyTestServer(t, "/search", 200, response, func(t *testing.T, body map[string]any) {
		if body["topic"] != "news" {
			t.Fatalf("expected topic=news, got %v", body["topic"])
		}
		if body["time_range"] != "week" {
			t.Fatalf("expected time_range=week, got %v", body["time_range"])
		}
		if body["search_depth"] != "advanced" {
			t.Fatalf("expected search_depth=advanced, got %v", body["search_depth"])
		}
		domains, ok := body["include_domains"].([]any)
		if !ok || len(domains) != 1 || domains[0] != "wikipedia.org" {
			t.Fatalf("expected include_domains=[wikipedia.org], got %v", body["include_domains"])
		}
	})
	defer srv.Close()

	reg := tavilyTestReg(t, srv.URL, "test-key")
	ctx := context.Background()
	_, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"web","query":"test","search_depth":"advanced","topic":"news","time_range":"week","include_domains":["wikipedia.org"]}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestTavilySearchHTTPError(t *testing.T) {
	srv := tavilyTestServer(t, "/search", 403, `{"error":"insufficient credits"}`, nil)
	defer srv.Close()

	reg := tavilyTestReg(t, srv.URL, "test-key")
	ctx := context.Background()
	// Tavily 403 falls through to empty webEngine list → returns fallback.
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"web","query":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no web results found") {
		t.Fatalf("expected fallback message, got: %q", out)
	}
}

func TestTavilySearchNoResults(t *testing.T) {
	srv := tavilyTestServer(t, "/search", 200, `{"results":[]}`, nil)
	defer srv.Close()

	reg := tavilyTestReg(t, srv.URL, "test-key")
	ctx := context.Background()
	// Empty Tavily results fall through to empty webEngine list → fallback.
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"web","query":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no web results found") {
		t.Fatalf("expected fallback message, got: %q", out)
	}
}

func TestTavilySearchNoKey(t *testing.T) {
	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&searchTool{
		ws: ws, maxLocalBytes: 256 * 1024, maxFetchKB: 100,
		httpClient: &http.Client{}, tavilyKey: "",
		webEngines: []webEngine{}, // no fallback to real engines
	})
	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"web","query":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no web results found") {
		t.Fatalf("expected fallback message, got: %q", out)
	}
}

func TestTavilyExtractSingle(t *testing.T) {
	response := `{"results":[{"url":"https://example.com","content":"Extracted page content here","raw_content":""}]}`
	srv := tavilyTestServer(t, "/extract", 200, response, nil)
	defer srv.Close()

	reg := tavilyTestReg(t, srv.URL, "test-key")
	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"extract","url":"https://example.com/article"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Extracted page content here") {
		t.Fatalf("expected content in output, got: %q", out)
	}
}

func TestTavilyExtractNoKey(t *testing.T) {
	ws, _ := setupWS(t)
	reg := NewRegistry()
	reg.Register(&searchTool{
		ws: ws, maxLocalBytes: 256 * 1024, maxFetchKB: 100,
		httpClient: &http.Client{}, tavilyKey: "",
		webEngines: []webEngine{},
	})
	ctx := context.Background()
	_, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"extract","url":"https://example.com"}`))
	if err == nil {
		t.Fatal("expected error when no Tavily key")
	}
	if !strings.Contains(err.Error(), "API_KEY") && !strings.Contains(err.Error(), "API key") {
		t.Fatalf("error should mention API key: %v", err)
	}
}

func TestTavilyExtractWithQuery(t *testing.T) {
	response := `{"results":[{"url":"https://example.com","content":"Reranked content based on query","raw_content":""}]}`
	srv := tavilyTestServer(t, "/extract", 200, response, func(t *testing.T, body map[string]any) {
		if body["query"] != "find the main points" {
			t.Fatalf("expected query='find the main points', got %v", body["query"])
		}
	})
	defer srv.Close()

	reg := tavilyTestReg(t, srv.URL, "test-key")
	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"extract","url":"https://example.com","query":"find the main points"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Reranked content") {
		t.Fatalf("expected content in output, got: %q", out)
	}
}

func TestTavilyExtractWithFormat(t *testing.T) {
	response := `{"results":[{"url":"https://example.com","content":"**markdown** content","raw_content":""}]}`
	srv := tavilyTestServer(t, "/extract", 200, response, func(t *testing.T, body map[string]any) {
		if body["format"] != "markdown" {
			t.Fatalf("expected format=markdown, got %v", body["format"])
		}
	})
	defer srv.Close()

	reg := tavilyTestReg(t, srv.URL, "test-key")
	ctx := context.Background()
	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"extract","url":"https://example.com","format":"markdown"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "markdown") {
		t.Fatalf("expected markdown content, got: %q", out)
	}
}
