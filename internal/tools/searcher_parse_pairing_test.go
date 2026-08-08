package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Defect C1 (DC-14/DC-9): parseDDGResults assumed every result-link row is
// followed by a result-snippet row. On rows [linkA][linkB][snippetB] the old
// `if pendingTitle == ""` guard skipped linkB entirely and the snippet attached
// to the stale linkA: one result "A" carrying snippet B, with B silently
// dropped. These tests drive the real entry point
// Execute -> searchWeb -> fetchWebEngine -> parseDDGResults -> guardWebResult
// through a stub webEngine over an httptest body.

// stubDDGLiteTool builds a keyless webSearchTool whose single engine is the
// DDG Lite parser served from srvURL.
func stubDDGLiteTool(t *testing.T, srvURL string, maxResultBytes int) *webSearchTool {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &webSearchTool{
		ws: ws,
		webEngines: []webEngine{{
			name:     "stub",
			buildURL: func(string) string { return srvURL },
			parse:    parseDDGResults,
		}},
		maxFetchKB:     100,
		maxResultBytes: maxResultBytes,
		httpClient:     &http.Client{Timeout: 5 * time.Second},
	}
}

func ddgLinkRow(href, title string) string {
	return `<tr><td class="result-link"><a href="` + href + `">` + title + `</a></td></tr>`
}

func ddgSnippetRow(s string) string {
	return `<tr><td class="result-snippet">` + s + `</td></tr>`
}

func ddgPair(href, title, snip string) string {
	return ddgLinkRow(href, title) + ddgSnippetRow(snip)
}

func TestSearchDDGLiteMissingSnippetPairing(t *testing.T) {
	// Rows [linkA][linkB][snippetB]: linkA has NO following snippet row. The
	// buggy parser emitted one result "A" carrying snippet B and dropped B.
	body := ddgLinkRow("https://a.example", "A") +
		ddgLinkRow("https://b.example", "B") +
		ddgSnippetRow("Snippet B")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	tool := stubDDGLiteTool(t, srv.URL, 256<<10)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"x","max_results":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, "• "); got != 2 {
		t.Fatalf("expected 2 results (both links must survive), got %d: %q", got, out)
	}
	if !strings.Contains(out, "https://a.example") || !strings.Contains(out, "https://b.example") {
		t.Fatalf("expected both result links present, got: %q", out)
	}
	aIdx := strings.Index(out, "• A")
	bIdx := strings.Index(out, "• B")
	if aIdx < 0 || bIdx < 0 {
		t.Fatalf("result blocks not found in: %q", out)
	}
	if strings.Contains(out[aIdx:bIdx], "Snippet B") {
		t.Fatalf("snippet B mispaired onto result A: %q", out)
	}
	if !strings.Contains(out[bIdx:], "Snippet B") {
		t.Fatalf("result B lost its own snippet: %q", out)
	}
}

func TestSearchDDGLiteParseStructuredInput(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := parseDDGResults("", 5); len(got) != 0 {
			t.Fatalf("empty HTML produced %d results: %v", len(got), got)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		inputs := []string{
			"<",
			`<tr><td class="result-link"><a href="https://a.com">A</a>`, // unclosed row
			ddgSnippetRow("snippet only"),                               // snippet with no link
			ddgLinkRow("", " ") + ddgSnippetRow("S"),                    // empty title+href then snippet
		}
		for _, in := range inputs {
			if got := parseDDGResults(in, 5); len(got) != 0 {
				t.Fatalf("malformed input %q produced %d results: %v", in, len(got), got)
			}
		}
	})
	t.Run("duplicate rows honor max", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < 50; i++ {
			b.WriteString(ddgPair("https://d.example", "D", "SD"))
		}
		if got := parseDDGResults(b.String(), 8); len(got) != 8 {
			t.Fatalf("max=8 produced %d results", len(got))
		}
	})
	t.Run("missing snippet boundary max1", func(t *testing.T) {
		// Flushing on a new link must honor the max bound: exactly 1 result.
		html := ddgLinkRow("https://a.example", "A") + ddgLinkRow("https://b.example", "B") + ddgSnippetRow("Snippet B")
		if got := parseDDGResults(html, 1); len(got) != 1 {
			t.Fatalf("max=1 produced %d results: %v", len(got), got)
		}
	})
	t.Run("link followed by two snippets", func(t *testing.T) {
		// A carries only the first snippet; the second is dropped, never
		// mispaired to a later link.
		html := ddgLinkRow("https://a.example", "A") + ddgSnippetRow("Snippet 1") + ddgSnippetRow("Snippet 2")
		got := parseDDGResults(html, 5)
		if len(got) != 1 {
			t.Fatalf("expected 1 result, got %d: %v", len(got), got)
		}
		if !strings.Contains(got[0], "Snippet 1") || strings.Contains(got[0], "Snippet 2") {
			t.Fatalf("mispairing across two snippets: %q", got[0])
		}
	})
	t.Run("entry point honors max_results", func(t *testing.T) {
		var b strings.Builder
		for i := 0; i < 50; i++ {
			b.WriteString(ddgPair("https://d.example", "D", "SD"))
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(b.String()))
		}))
		defer srv.Close()
		tool := stubDDGLiteTool(t, srv.URL, 256<<10)
		out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"x","max_results":3}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(out, "• "); got != 3 {
			t.Fatalf("max_results=3 produced %d results: %q", got, out)
		}
	})
}

func TestSearchDDGLiteOversizedBodyBounded(t *testing.T) {
	// Body far past the 100 KiB wire read, with a sentinel at the very end:
	// the result must be bounded by maxFetchKB (io.LimitReader) and by the
	// guardWebResult composed-result budget, and must never contain content
	// from beyond the wire cut.
	pair := ddgPair("https://a.example", "A", "snippet")
	var b strings.Builder
	for i := 0; i < 18000; i++ { // ~2.3 MB
		b.WriteString(pair)
	}
	b.WriteString("__TAIL_MARKER__")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	tool := stubDDGLiteTool(t, srv.URL, 256<<10)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"x","max_results":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("expected a non-empty result")
	}
	if len(out) > 256<<10 {
		t.Fatalf("result %d bytes exceeds the declared %d-byte budget", len(out), 256<<10)
	}
	if strings.Contains(out, "__TAIL_MARKER__") {
		t.Fatal("output contains content from beyond the maxFetchKB wire read")
	}
}

func TestSearchDDGLiteBudgetRefusesOverBound(t *testing.T) {
	// A composed result over maxResultBytes must be refused loudly
	// (errWebResponseBudget), never handed back as a partial document.
	pair := ddgLinkRow("https://a.example", "A") + ddgSnippetRow(strings.Repeat("s", 80))
	var b strings.Builder
	for i := 0; i < 6; i++ {
		b.WriteString(pair)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(b.String()))
	}))
	defer srv.Close()

	tool := stubDDGLiteTool(t, srv.URL, 512)
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"x","max_results":5}`))
	if err == nil {
		t.Fatal("expected an over-budget refusal")
	}
	if !errors.Is(err, errWebResponseBudget) {
		t.Fatalf("expected errWebResponseBudget, got %v", err)
	}
}

// FuzzParseDDGResults pins the fix's flush path to the same bounds the
// snippet path honors: no panic, len(out) <= max when max > 0, len(out) <= 1
// when max <= 0 (bounded, never unbounded), and no empty results.
func FuzzParseDDGResults(f *testing.F) {
	pair := ddgPair("https://a.example", "A", "Snippet A")
	seeds := []struct {
		html string
		max  int
	}{
		{"", 8},   // empty
		{pair, 8}, // well-formed alternating rows
		{ddgLinkRow("https://a.example", "A") + ddgLinkRow("https://b.example", "B") + ddgSnippetRow("Snippet B"), 8}, // missing-snippet interleaving
		{"<", 8}, // bare '<'
		{`<tr><td class="result-link"><a href="https://a.com">A</a>`, 8}, // unclosed tag
		{ddgSnippetRow("snippet only"), 8},                               // snippet-only row
		{strings.Repeat(pair, 50), 8},                                    // duplicate rows
		{strings.Repeat(pair, 550), 8},                                   // ~64 KiB repeated block
		{"", 0},
		{"", -1},
	}
	for _, s := range seeds {
		f.Add(s.html, s.max)
	}
	f.Fuzz(func(t *testing.T, html string, max int) {
		out := parseDDGResults(html, max)
		if max > 0 && len(out) > max {
			t.Fatalf("parseDDGResults returned %d results for max=%d", len(out), max)
		}
		if max <= 0 && len(out) > 1 {
			t.Fatalf("parseDDGResults returned %d results for max=%d", len(out), max)
		}
		for i, r := range out {
			if r == "" {
				t.Fatalf("result %d is empty", i)
			}
		}
	})
}
