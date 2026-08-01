package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tavilyBodyServer serves a fixed 200 response on any path.
func tavilyBodyServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// searchResponseJSON builds a Tavily search response whose answer field is
// exactly answerLen bytes of a repeating marker.
func searchResponseJSON(t *testing.T, answerLen int) (raw, answer string) {
	t.Helper()
	answer = strings.Repeat("A", answerLen)
	body, err := json.Marshal(tavilySearchResponse{
		Results: []tavilySearchResult{{Title: "T", URL: "https://example.test/p", Content: "c"}},
		Answer:  answer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body), answer
}

// extractResponseJSON builds a Tavily extract response whose content field is
// exactly contentLen bytes of a repeating marker.
func extractResponseJSON(t *testing.T, contentLen int) (raw, content string) {
	t.Helper()
	content = strings.Repeat("C", contentLen)
	body, err := json.Marshal(tavilyExtractResponse{
		Results: []tavilyExtractResult{{URL: "https://example.test/p", Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body), content
}

func newBudgetedSearchTool(t *testing.T, srv *httptest.Server, budget int) *webSearchTool {
	t.Helper()
	ws, _ := setupWS(t)
	return &webSearchTool{
		ws: ws, maxFetchKB: 100, httpClient: &http.Client{},
		tavilyKey: "test-key", tavilyBaseURL: srv.URL,
		// No free-engine fallback chain: a budget refusal must surface as
		// itself, never as substituted results from another engine.
		webEngines:     []webEngine{},
		maxResultBytes: budget,
	}
}

func newBudgetedExtractTool(t *testing.T, srv *httptest.Server, budget int) *extractTool {
	t.Helper()
	return &extractTool{
		tavilyKey: "test-key", tavilyBaseURL: srv.URL,
		httpClient: &http.Client{}, maxResultBytes: budget,
	}
}

// assertBudgetError checks the refusal names the byte bound and the config key
// that raises it. An operator who cannot act on the message has been told
// nothing.
func assertBudgetError(t *testing.T, err error, limit int) {
	t.Helper()
	if err == nil {
		t.Fatal("over-bound response was accepted; want an explicit refusal")
	}
	if !errors.Is(err, errWebResponseBudget) {
		t.Fatalf("error %q is not errWebResponseBudget", err)
	}
	if msg := err.Error(); !strings.Contains(msg, fmt.Sprint(limit)) ||
		!strings.Contains(msg, "max_tavily_response_bytes") {
		t.Fatalf("refusal %q must name the %d-byte bound and max_tavily_response_bytes", msg, limit)
	}
}

// The bound is inclusive: a body of exactly limit bytes is accepted, and the
// very next byte is refused. limit+1 is read precisely so this edge is exact
// rather than approximate.
func TestWireBoundIsExactAtTheBoundary(t *testing.T) {
	const limit = 1000
	for _, c := range []struct {
		size   int
		accept bool
	}{{limit - 1, true}, {limit, true}, {limit + 1, false}} {
		got, err := readWebResponse(strings.NewReader(strings.Repeat("x", c.size)), limit, "search")
		if c.accept {
			if err != nil {
				t.Errorf("body of %d bytes refused at limit %d: %v", c.size, limit, err)
				continue
			}
			if len(got) != c.size {
				t.Errorf("body of %d bytes read back as %d", c.size, len(got))
			}
			continue
		}
		if err == nil {
			t.Errorf("body of %d bytes accepted at limit %d", c.size, limit)
			continue
		}
		assertBudgetError(t, err, limit)
	}
}

// A non-positive bound is a tool built without one; it must fall back to the
// built-in default rather than refuse everything or read without limit.
func TestWireBoundFallsBackToTheDefault(t *testing.T) {
	for _, limit := range []int{0, -1} {
		got, err := readWebResponse(strings.NewReader("{}"), limit, "search")
		if err != nil {
			t.Errorf("limit %d refused a 2-byte body: %v", limit, err)
			continue
		}
		if string(got) != "{}" {
			t.Errorf("limit %d read back %q", limit, got)
		}
	}
}

func TestSearchRefusesOverBoundResponseBody(t *testing.T) {
	const budget = 4096
	raw, _ := searchResponseJSON(t, budget)
	if len(raw) <= budget {
		t.Fatalf("fixture body is %d bytes, must exceed the %d-byte bound", len(raw), budget)
	}
	tool := newBudgetedSearchTool(t, tavilyBodyServer(t, raw), budget)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`))
	assertBudgetError(t, err, budget)
}

// The amplification case: `search` writes a 4-byte bullet per result against a
// 3-byte empty JSON object, so a body comfortably under the bound composes to
// ~5/3 of it. The wire limit alone does not make the declared budget true.
func TestSearchRefusesOverBoundComposedResult(t *testing.T) {
	const budget = 100_000
	const results = 30_000
	raw := `{"results":[` + strings.TrimSuffix(strings.Repeat("{},", results), ",") + `]}`
	if len(raw) > budget {
		t.Fatalf("fixture body is %d bytes; the point of this test is a body UNDER the %d-byte bound", len(raw), budget)
	}
	tool := newBudgetedSearchTool(t, tavilyBodyServer(t, raw), budget)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`))
	assertBudgetError(t, err, budget)
}

// The header formats the model-supplied query with %q, which expands 0x7f to
// four bytes. A query that passes the dispatcher's input gate can therefore
// blow past the budget through framing alone.
func TestSearchRefusesOverBoundHeaderExpansion(t *testing.T) {
	const budget = 4096
	raw, _ := searchResponseJSON(t, 8)
	tool := newBudgetedSearchTool(t, tavilyBodyServer(t, raw), budget)

	query := strings.Repeat("", 2000) // %q renders each as \x7f: 4 bytes
	args, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), json.RawMessage(args))
	assertBudgetError(t, err, budget)
}

func TestSearchReturnsUnderBoundAnswerWhole(t *testing.T) {
	const budget = 100_000
	raw, answer := searchResponseJSON(t, 50_000)
	if len(raw) > budget {
		t.Fatalf("fixture body %d exceeds the bound %d", len(raw), budget)
	}
	tool := newBudgetedSearchTool(t, tavilyBodyServer(t, raw), budget)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, answer) {
		t.Fatalf("answer did not arrive whole: result is %d bytes, answer is %d", len(out), len(answer))
	}
}

func TestExtractRefusesOverBoundResponseBody(t *testing.T) {
	const budget = 4096
	raw, _ := extractResponseJSON(t, budget)
	if len(raw) <= budget {
		t.Fatalf("fixture body is %d bytes, must exceed the %d-byte bound", len(raw), budget)
	}
	tool := newBudgetedExtractTool(t, tavilyBodyServer(t, raw), budget)

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.test/p"}`))
	assertBudgetError(t, err, budget)
}

func TestExtractReturnsUnderBoundContentWhole(t *testing.T) {
	const budget = 100_000
	raw, content := extractResponseJSON(t, 50_000)
	if len(raw) > budget {
		t.Fatalf("fixture body %d exceeds the bound %d", len(raw), budget)
	}
	tool := newBudgetedExtractTool(t, tavilyBodyServer(t, raw), budget)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.test/p"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, content) {
		t.Fatalf("content did not arrive whole: result is %d bytes, content is %d", len(out), len(content))
	}
}

// A budget refusal must not be laundered into free-engine results: `search`
// falls back on any Tavily error, and a silent substitution is precisely the
// "you were told nothing" failure the explicit bound exists to prevent.
func TestSearchBudgetRefusalIsNotSwallowedByFreeEngineFallback(t *testing.T) {
	const budget = 4096
	raw, _ := searchResponseJSON(t, budget)
	ws, _ := setupWS(t)
	tool := &webSearchTool{
		ws: ws, maxFetchKB: 100, httpClient: &http.Client{},
		tavilyKey: "test-key", tavilyBaseURL: tavilyBodyServer(t, raw).URL,
		maxResultBytes: budget,
		webEngines: []webEngine{{
			name:     "stub",
			buildURL: func(string) string { return tavilyBodyServer(t, "unused").URL },
			parse:    func(string, int) []string { return []string{"substituted result"} },
		}},
	}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`))
	if err == nil {
		t.Fatalf("budget refusal was replaced by fallback results: %q", out)
	}
	assertBudgetError(t, err, budget)
}

// The free-engine fallback composes its own result and returns it directly.
// That path is reached on ANY non-budget Tavily failure and is the ONLY path
// a keyless install can take, so leaving it unguarded makes ResultBudgetBytes()
// false - the declaration the whole design rests on.
func TestSearchFreeEngineResultIsGuarded(t *testing.T) {
	const budget = 4096
	ws, _ := setupWS(t)
	srv := tavilyBodyServer(t, "<html></html>")
	tool := &webSearchTool{
		ws: ws, maxFetchKB: 100, httpClient: &http.Client{},
		maxResultBytes: budget, // no tavilyKey: free engines are the only path
		webEngines: []webEngine{{
			name:     "stub",
			buildURL: func(string) string { return srv.URL },
			parse: func(string, int) []string {
				out := make([]string, 200)
				for i := range out {
					out[i] = strings.Repeat("r", 100)
				}
				return out // ~20100 bytes joined, far over the bound
			},
		}},
	}

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`))
	assertBudgetError(t, err, budget)
}

// extract's empty-content path returns an echo of the model-supplied url
// without passing through the guard. The url is unbounded by the response
// budget, so this return can exceed the declared budget on its own.
// The echoed URL falls back to the model-supplied argument only when the
// provider omits its own - that fallback is the one remaining path where an
// unbounded request-side string reaches the result, so it is what this pins.
// When the provider does supply a URL (the normal case) extract now echoes
// that instead, so the unbounded model argument never reaches the output at
// all; see TestExtractPrefersProviderURLOverUnboundedArgument.
func TestExtractEmptyContentPathIsGuarded(t *testing.T) {
	const budget = 1024
	raw, err := json.Marshal(tavilyExtractResponse{
		Results: []tavilyExtractResult{{URL: "", Content: "", RawContent: ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := newBudgetedExtractTool(t, tavilyBodyServer(t, string(raw)), budget)

	longURL := "https://example.test/" + strings.Repeat("u", 1100)
	args, err := json.Marshal(map[string]string{"url": longURL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Execute(context.Background(), json.RawMessage(args))
	assertBudgetError(t, err, budget)
}

// "do not truncate responses at all": every result's text must survive whole,
// not just the answer field. formatWebResult used to cut each snippet to 150
// bytes, which made the never-truncates claim false for `search`.
func TestSearchResultContentSurvivesWhole(t *testing.T) {
	const budget = 100_000
	// No leading/trailing whitespace: formatWebResult trims the field, which is
	// normalization, not truncation, and would otherwise mask the assertion.
	content := strings.TrimSpace(strings.Repeat("per-result-content ", 500)) // >> 150 bytes
	raw, err := json.Marshal(tavilySearchResponse{
		Results: []tavilySearchResult{{Title: "T", URL: "https://example.test/p", Content: content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := newBudgetedSearchTool(t, tavilyBodyServer(t, string(raw)), budget)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, content) {
		t.Fatalf("per-result content was cut: result is %d bytes, content is %d", len(out), len(content))
	}
}

// The same for the free-engine path, whose snippets came from the same cut.
func TestFreeEngineSnippetSurvivesWhole(t *testing.T) {
	snippet := strings.Repeat("s", 4000)
	got := formatWebResult("title", "https://example.test/p", snippet)
	if !strings.Contains(got, snippet) {
		t.Fatalf("snippet was cut: formatted result is %d bytes, snippet is %d", len(got), len(snippet))
	}
}

// ResultBudgetBytes is what the dispatcher derives its backstop from; it must
// report the configured number, and the built-in default when unset.
func TestTavilyToolsDeclareTheConfiguredBudget(t *testing.T) {
	ws, _ := setupWS(t)
	cases := []struct{ set, want int }{
		{set: 123_456, want: 123_456},
		{set: 0, want: defaultTavilyResponseBytes},
		{set: -1, want: defaultTavilyResponseBytes},
	}
	for _, c := range cases {
		search := &webSearchTool{ws: ws, tavilyKey: "k", maxResultBytes: c.set}
		if got := search.ResultBudgetBytes(); got != c.want {
			t.Errorf("search.ResultBudgetBytes() with maxResultBytes=%d = %d, want %d", c.set, got, c.want)
		}
		extract := &extractTool{tavilyKey: "k", maxResultBytes: c.set}
		if got := extract.ResultBudgetBytes(); got != c.want {
			t.Errorf("extract.ResultBudgetBytes() with maxResultBytes=%d = %d, want %d", c.set, got, c.want)
		}
	}
}

// The registry decides the budget: with no provider key neither tool can reach
// the provider, so neither may inflate the single global dispatcher ceiling.
func TestKeylessRegistryDeclaresNoProviderSizedBudget(t *testing.T) {
	ws, _ := setupWS(t)
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, MaxTavilyResponseBytes: 4 << 20})
	for name, want := range map[string]int{
		"search":  freeEngineResultBudget, // the only reachable path is the free-engine chain
		"extract": keylessToolResultBudget,
	} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		budgeted, ok := tool.(ResultBudgetTool)
		if !ok {
			t.Fatalf("%s does not implement ResultBudgetTool", name)
		}
		if got := budgeted.ResultBudgetBytes(); got != want {
			t.Errorf("keyless %s declares %d, want %d", name, got, want)
		}
	}
}

// With a key configured, both tools declare the configured bound.
func TestKeyedRegistryDeclaresConfiguredBudget(t *testing.T) {
	ws, _ := setupWS(t)
	const budget = 2 << 20
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace: ws, TavilyAPIKey: "k", MaxTavilyResponseBytes: budget,
	})
	for _, name := range []string{"search", "extract"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		budgeted, ok := tool.(ResultBudgetTool)
		if !ok {
			t.Fatalf("%s does not implement ResultBudgetTool", name)
		}
		if got := budgeted.ResultBudgetBytes(); got != budget {
			t.Errorf("%s declares %d, want the configured %d", name, got, budget)
		}
	}
}
