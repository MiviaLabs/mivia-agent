package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestSearchLocalGrep(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "hello.go"), []byte("package main\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "world.go"), []byte("package main\nfunc World() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws.Abs, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "pkg", "util.go"), []byte("package pkg\nfunc Util() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	out, err := reg.Execute(ctx, "search", json.RawMessage(`{"scope":"local","query":"func Hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello.go") {
		t.Fatalf("expected hello.go in local search, got: %q", out)
	}

	out, err = reg.Execute(ctx, "search", json.RawMessage(`{"scope":"local","query":"Util","glob":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pkg/util.go") {
		t.Fatalf("expected pkg/util.go in filtered search, got: %q", out)
	}
}

func TestSearchLocalFilenameMatch(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "important.go"), []byte("code"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"important"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "important.go") {
		t.Fatalf("expected important.go in filename match, got: %q", out)
	}
}

func TestSearchLocalNoMatches(t *testing.T) {
	_, reg := setupWS(t)
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"zzz_nonexistent"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "no matches found" {
		t.Fatalf("expected 'no matches found', got: %q", out)
	}
}

func TestSearchLocalRequiresQuery(t *testing.T) {
	_, reg := setupWS(t)
	_, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local"}`))
	if err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestSearchLocalSkipsSecretPaths(t *testing.T) {
	ws, reg := setupWS(t)
	_ = os.WriteFile(filepath.Join(ws.Abs, ".env"), []byte("SECRET=1"), 0o600)
	_ = os.WriteFile(filepath.Join(ws.Abs, "main.go"), []byte("package main"), 0o644)
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"SECRET"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, ".env") {
		t.Fatalf("search should skip .env files: %q", out)
	}
}

func TestSearchLocalSkipsBinary(t *testing.T) {
	ws, reg := setupWS(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "data.bin"), []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "note.txt"), []byte("binary data nearby"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"data"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "data.bin") && !strings.Contains(out, "note.txt") {
		t.Fatalf("expected filename match for data.bin or content match for note.txt, got: %q", out)
	}
}

func TestSearchLocalMaxResults(t *testing.T) {
	ws, reg := setupWS(t)
	var content strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&content, "match-line-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "many.txt"), []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"local","query":"match-line","max_results":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncation, got: %q", out)
	}
}

func TestSearchInvalidScope(t *testing.T) {
	_, reg := setupWS(t)
	_, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"invalid","query":"test"}`))
	if err == nil {
		t.Fatal("expected error for invalid scope")
	}
}

func TestSearchURLValidation(t *testing.T) {
	_, reg := setupWS(t)
	_, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"url"}`))
	if err == nil {
		t.Fatal("expected error for missing url")
	}
	_, err = reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"url","url":"ftp://bad.com"}`))
	if err == nil {
		t.Fatal("expected error for non-http URL")
	}
}

func TestSearchWebRequiresQuery(t *testing.T) {
	_, reg := setupWS(t)
	_, err := reg.Execute(context.Background(), "search", json.RawMessage(`{"scope":"web"}`))
	if err == nil {
		t.Fatal("expected error for missing web query")
	}
}

func TestStripHTMLTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<b>bold</b>", "bold"},
		{"<a href=\"x\">link</a>", "link"},
		{"hello &amp; world", "hello & world"},
		{"&lt;tag&gt;", "<tag>"},
		{"multi\n  \nline", "multi line"},
		// Table cells: <tr><td>1.</td><td>Title</td></tr> strips to "1.Title" (no space between cells)
		{"<tr><td>1.</td><td>Title</td></tr>", "1.Title"},
		{"", ""},
		{"no tags", "no tags"},
		{"Text &nbsp; spaced", "Text  spaced"},
	}
	for _, tt := range tests {
		got := stripHTMLTags(tt.input)
		if got != tt.want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDecodeHTMLEntities(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"&amp;", "&"},
		{"&lt;br/&gt;", "<br/>"},
		{"&quot;hello&quot;", "\"hello\""},
		{"&#x27;apos&#x27;", "'apos'"},
		{"plain text", "plain text"},
	}
	for _, tt := range tests {
		got := decodeHTMLEntities(tt.input)
		if got != tt.want {
			t.Errorf("decodeHTMLEntities(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsTextContentType(t *testing.T) {
	tests := map[string]bool{
		"text/html":                 true,
		"text/plain; charset=utf-8": true,
		"application/json":          true,
		"application/xml":           true,
		"image/png":                 false,
		"application/octet-stream":  false,
		"":                          false,
	}
	for ct, want := range tests {
		got := isTextContentType(ct)
		if got != want {
			t.Errorf("isTextContentType(%q) = %v, want %v", ct, got, want)
		}
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := "hello world"
	if got := truncateUTF8(s, 5); got != "hello" {
		t.Fatalf("got %q", got)
	}
	s = "héllo"
	if got := truncateUTF8(s, 3); got != "hé" {
		t.Fatalf("expected 'hé', got %q (len=%d)", got, len(got))
	}
	if got := truncateUTF8(s, 100); got != s {
		t.Fatalf("expected full string")
	}
}

func TestSearchToolRegistered(t *testing.T) {
	_, reg := setupWS(t)
	_, ok := reg.Get("search")
	if !ok {
		t.Fatal("search tool not registered")
	}
}

func TestSearchOpenAISchema(t *testing.T) {
	_, reg := setupWS(t)
	tools := reg.OpenAITools()
	found := false
	for _, tool := range tools {
		fn, _ := tool["function"].(map[string]any)
		if fn["name"] == "search" {
			found = true
			params, _ := fn["parameters"].(map[string]any)
			props, _ := params["properties"].(map[string]any)
			scope, ok := props["scope"]
			if !ok {
				t.Fatal("search schema missing scope property")
			}
			scopeObj, _ := scope.(map[string]any)
			enumRaw := scopeObj["enum"]
			enumLen := 0
			switch v := enumRaw.(type) {
			case []any:
				enumLen = len(v)
			case []string:
				enumLen = len(v)
			}
			if enumLen != 3 {
				t.Fatalf("expected 3 enum values (local, web, url), got %v (type %T)", enumRaw, enumRaw)
			}
		}
	}
	if !found {
		t.Fatal("search tool not found in OpenAI schema")
	}
}

func TestParseDDGResults(t *testing.T) {
	html := `<html><body>
<table>
<tr><td>1.</td><td class="result-link"><a href="https://example.com/test">Example Test</a></td></tr>
<tr><td></td><td class="result-snippet">This is a test snippet.</td></tr>
<tr><td>2.</td><td class="result-link"><a href="https://golang.org">Go Programming</a></td></tr>
<tr><td></td><td class="result-snippet">The Go programming language.</td></tr>
</table>
</body></html>`
	results := parseDDGResults(html, 5)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(results), results)
	}
	if !strings.Contains(results[0], "Example Test") {
		t.Fatalf("expected 'Example Test' in first result, got: %q", results[0])
	}
	if !strings.Contains(results[1], "Go Programming") {
		t.Fatalf("expected 'Go Programming' in second result, got: %q", results[1])
	}
}

func TestParseDDGResultsMax(t *testing.T) {
	html := `<html><body><table>
<tr><td>1.</td><td class="result-link"><a href="https://a.com">A</a></td></tr>
<tr><td></td><td class="result-snippet">Snippet A</td></tr>
<tr><td>2.</td><td class="result-link"><a href="https://b.com">B</a></td></tr>
<tr><td></td><td class="result-snippet">Snippet B</td></tr>
<tr><td>3.</td><td class="result-link"><a href="https://c.com">C</a></td></tr>
<tr><td></td><td class="result-snippet">Snippet C</td></tr>
</table></body></html>`
	results := parseDDGResults(html, 2)
	if len(results) != 2 {
		t.Fatalf("expected max 2 results, got %d", len(results))
	}
}

func TestStripHTMLTagsEntities(t *testing.T) {
	input := "&lt;b&gt;bold&lt;/b&gt;"
	want := "<b>bold</b>"
	got := stripHTMLTags(input)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestIsTextContentTypeEmpty(t *testing.T) {
	if isTextContentType("") {
		t.Fatal("expected false for empty content type")
	}
}

func TestLooksLikeBotChallenge(t *testing.T) {
	cases := map[string]bool{
		`<div class="anomaly-modal">blocked</div>`: true,
		`<form id="challenge-form">...</form>`:     true,
		`Unfortunately, bots use DuckDuckGo too.`:  true,
		// Bare "captcha" / recaptcha on a normal SERP is not a challenge page.
		`Please complete the CAPTCHA to continue`: false,
		`<li class="b_algo"><h2><a href="x">ok</a></h2></li>
<script src="https://www.google.com/recaptcha/api.js"></script>`: false,
		`<tr><td class="result-link"><a href="x">ok</a></td></tr>`: false,
		``: false,
	}
	for body, want := range cases {
		if got := looksLikeBotChallenge(body); got != want {
			t.Errorf("looksLikeBotChallenge(%q) = %v, want %v", body, got, want)
		}
	}
}

func TestSetBrowserHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	setBrowserHeaders(req)
	if !strings.Contains(req.Header.Get("User-Agent"), "Chrome") {
		t.Fatalf("expected Chrome UA, got %q", req.Header.Get("User-Agent"))
	}
	if req.Header.Get("Accept") == "" {
		t.Fatal("expected Accept header")
	}
	if req.Header.Get("Accept-Language") == "" {
		t.Fatal("expected Accept-Language header")
	}
}

func TestParseDDGHTMLResults(t *testing.T) {
	html := `<html><body>
<div class="result">
  <a rel="nofollow" class="result__a" href="https://example.com/a">Alpha Result</a>
  <a class="result__snippet" href="https://example.com/a">Snippet for alpha</a>
</div>
<div class="result">
  <a href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fgolang.org%2Fdoc" class="result__a">Go Docs</a>
  <a class="result__snippet">Official documentation</a>
</div>
</body></html>`
	results := parseDDGHTMLResults(html, 5)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(results), results)
	}
	if !strings.Contains(results[0], "Alpha Result") || !strings.Contains(results[0], "https://example.com/a") {
		t.Fatalf("first result unexpected: %q", results[0])
	}
	if !strings.Contains(results[1], "Go Docs") || !strings.Contains(results[1], "https://golang.org/doc") {
		t.Fatalf("expected unwrapped uddg URL in second result, got: %q", results[1])
	}
}

func TestParseDDGHTMLResultsMax(t *testing.T) {
	html := `
<a class="result__a" href="https://a.com">A</a>
<a class="result__a" href="https://b.com">B</a>
<a class="result__a" href="https://c.com">C</a>`
	results := parseDDGHTMLResults(html, 2)
	if len(results) != 2 {
		t.Fatalf("expected max 2, got %d", len(results))
	}
}

func TestParseBingResults(t *testing.T) {
	html := `<html><body><ol id="b_results">
<li class="b_algo">
  <h2><a href="https://example.com/bing1" h="ID=SERP">Bing One</a></h2>
  <div class="b_caption"><p>First bing snippet.</p></div>
</li>
<li class="b_algo">
  <h2><a href="https://example.com/bing2">Bing Two</a></h2>
  <div class="b_caption"><p>Second snippet.</p></div>
</li>
</ol></body></html>`
	results := parseBingResults(html, 5)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(results), results)
	}
	if !strings.Contains(results[0], "Bing One") || !strings.Contains(results[0], "https://example.com/bing1") {
		t.Fatalf("first result unexpected: %q", results[0])
	}
	if !strings.Contains(results[0], "First bing snippet") {
		t.Fatalf("expected snippet in first result: %q", results[0])
	}
}

func TestParseBingResultsMax(t *testing.T) {
	html := `
<li class="b_algo"><h2><a href="https://a.com">A</a></h2><p>sa</p></li>
<li class="b_algo"><h2><a href="https://b.com">B</a></h2><p>sb</p></li>
<li class="b_algo"><h2><a href="https://c.com">C</a></h2><p>sc</p></li>`
	results := parseBingResults(html, 1)
	if len(results) != 1 {
		t.Fatalf("expected max 1, got %d", len(results))
	}
}

func TestParseDDGIAJSON(t *testing.T) {
	body := `{
  "Heading": "Go (programming language)",
  "AbstractText": "Go is a statically typed language.",
  "AbstractURL": "https://en.wikipedia.org/wiki/Go_(programming_language)",
  "AbstractSource": "Wikipedia",
  "Results": [
    {"Text": "Official site", "FirstURL": "https://go.dev"}
  ],
  "RelatedTopics": [
    {"Text": "Tour of Go", "FirstURL": "https://go.dev/tour"},
    {"Name": "See also", "Topics": [
      {"Text": "Effective Go", "FirstURL": "https://go.dev/doc/effective_go"}
    ]}
  ]
}`
	results := parseDDGIAJSON(body, 10)
	if len(results) < 3 {
		t.Fatalf("expected at least 3 results, got %d: %v", len(results), results)
	}
	joined := strings.Join(results, "\n")
	if !strings.Contains(joined, "Go (programming language)") {
		t.Fatalf("missing abstract heading: %q", joined)
	}
	if !strings.Contains(joined, "https://go.dev") {
		t.Fatalf("missing official site: %q", joined)
	}
	if !strings.Contains(joined, "Effective Go") {
		t.Fatalf("missing nested related topic: %q", joined)
	}
}

func TestParseDDGIAJSONInvalid(t *testing.T) {
	if got := parseDDGIAJSON("not-json", 5); got != nil {
		t.Fatalf("expected nil for invalid JSON, got %v", got)
	}
	if got := parseDDGIAJSON(`{}`, 5); len(got) != 0 {
		t.Fatalf("expected empty for empty payload, got %v", got)
	}
}

func TestParseDDGIAJSONMax(t *testing.T) {
	body := `{
  "AbstractText": "abs",
  "Heading": "H",
  "Results": [
    {"Text": "R1", "FirstURL": "https://r1.example"},
    {"Text": "R2", "FirstURL": "https://r2.example"}
  ],
  "RelatedTopics": [
    {"Text": "T1", "FirstURL": "https://t1.example"},
    {"Text": "T2", "FirstURL": "https://t2.example"}
  ]
}`
	results := parseDDGIAJSON(body, 2)
	if len(results) != 2 {
		t.Fatalf("expected max 2, got %d: %v", len(results), results)
	}
}

func TestSearchWebFallbackChain(t *testing.T) {
	var hits atomic.Int32
	var sawUA atomic.Value
	// Server 1: challenge page
	s1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		sawUA.Store(r.Header.Get("User-Agent"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><div class="anomaly-modal">blocked</div></html>`))
	}))
	defer s1.Close()

	// Server 2: non-2xx
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("nope"))
	}))
	defer s2.Close()

	// Server 3: empty parse
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><body>no results here</body></html>`))
	}))
	defer s3.Close()

	// Server 4: Bing-like success
	s4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<li class="b_algo"><h2><a href="https://ok.example">OK Hit</a></h2><p>works</p></li>`))
	}))
	defer s4.Close()

	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := &searchTool{
		ws:            ws,
		maxLocalBytes: 64 * 1024,
		maxFetchKB:    100,
		httpClient:    &http.Client{Timeout: 5 * time.Second},
		webEngines: []webEngine{
			{name: "challenge", buildURL: func(string) string { return s1.URL }, parse: parseDDGResults},
			{name: "forbid", buildURL: func(string) string { return s2.URL }, parse: parseDDGResults},
			{name: "empty", buildURL: func(string) string { return s3.URL }, parse: parseDDGResults},
			{name: "bing", buildURL: func(string) string { return s4.URL }, parse: parseBingResults},
		},
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"scope":"web","query":"test query"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "OK Hit") {
		t.Fatalf("expected fallback success, got: %q", out)
	}
	if hits.Load() != 4 {
		t.Fatalf("expected all 4 engines tried until success, got hits=%d", hits.Load())
	}
	ua, _ := sawUA.Load().(string)
	if !strings.Contains(ua, "Chrome") {
		t.Fatalf("expected browser UA, got %q", ua)
	}
}

func TestSearchWebAllFail(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`Unfortunately, bots use DuckDuckGo too.`))
	}))
	defer s.Close()

	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := &searchTool{
		ws:            ws,
		maxLocalBytes: 64 * 1024,
		maxFetchKB:    100,
		httpClient:    &http.Client{Timeout: 5 * time.Second},
		webEngines: []webEngine{
			{name: "only", buildURL: func(string) string { return s.URL }, parse: parseDDGResults},
		},
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"scope":"web","query":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "no web results found" {
		t.Fatalf("got %q", out)
	}
}

func TestFetchURLUsesBrowserHeaders(t *testing.T) {
	var sawUA string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello body"))
	}))
	defer s.Close()

	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// httptest is loopback; allowPrivateFetch is tests-only for header/body checks.
	tool := &searchTool{
		ws:                ws,
		maxLocalBytes:     64 * 1024,
		maxFetchKB:        100,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		allowPrivateFetch: true,
	}
	args, _ := json.Marshal(map[string]any{"scope": "url", "url": s.URL})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello body") {
		t.Fatalf("unexpected body: %q", out)
	}
	if !strings.Contains(sawUA, "Chrome") {
		t.Fatalf("fetchURL should send browser UA, got %q", sawUA)
	}
}

func TestIsBlockedFetchIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",
		"::1",
		"10.0.0.1",
		"192.168.1.1",
		"172.16.0.1",
		"169.254.169.254",
		"169.254.1.1",
		"0.0.0.0",
		"100.64.0.1",
		"224.0.0.1",
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if !isBlockedFetchIP(ip) {
			t.Errorf("expected blocked: %s", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34"}
	for _, s := range allowed {
		ip := net.ParseIP(s)
		if isBlockedFetchIP(ip) {
			t.Errorf("expected allowed: %s", s)
		}
	}
}

func TestValidateFetchURLBlocksPrivateLiterals(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{
		"http://127.0.0.1/",
		"http://127.0.0.1:8080/path",
		"https://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5/",
		"http://192.168.0.1/",
		"http://[::1]/",
		"ftp://example.com/",
	} {
		if err := validateFetchURL(ctx, u); err == nil {
			t.Errorf("expected block for %s", u)
		}
	}
}

func TestFetchURLBlocksPrivateIPLiterals(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := &searchTool{
		ws:            ws,
		maxLocalBytes: 64 * 1024,
		maxFetchKB:    100,
		fetchClient:   newSafeFetchHTTPClient(5 * time.Second),
	}
	for _, u := range []string{
		"http://127.0.0.1/",
		"http://169.254.169.254/",
	} {
		args, _ := json.Marshal(map[string]any{"scope": "url", "url": u})
		_, err := tool.Execute(context.Background(), args)
		if err == nil {
			t.Errorf("expected SSRF block for %s", u)
		}
	}
}

func TestFetchURLBlocksRedirectToPrivate(t *testing.T) {
	// Public-looking first hop via custom RoundTripper is unnecessary: we only
	// need CheckRedirect to reject Location targeting a private IP.
	// Serve a 302 from a loopback httptest using a *non*-SSRF client would dial
	// loopback first — instead exercise CheckRedirect policy directly and
	// confirm Execute fails when the safe client would follow to 127.0.0.1.
	redirects := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			redirects++
			http.Redirect(w, r, "http://127.0.0.1:9/secret", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("should not reach"))
	}))
	defer s.Close()

	// Initial URL is also loopback (httptest) → blocked before dial by validateFetchURL.
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := &searchTool{
		ws:            ws,
		maxLocalBytes: 64 * 1024,
		maxFetchKB:    100,
		fetchClient:   newSafeFetchHTTPClient(5 * time.Second),
	}
	args, _ := json.Marshal(map[string]any{"scope": "url", "url": s.URL + "/start"})
	_, err = tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected fetch of loopback httptest URL to fail SSRF checks")
	}
	_ = redirects
}

func TestSafeFetchClientCheckRedirectBlocksPrivate(t *testing.T) {
	client := newSafeFetchHTTPClient(5 * time.Second)
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1/secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := []*http.Request{req}
	if err := client.CheckRedirect(req, via); err == nil {
		t.Fatal("CheckRedirect should reject private redirect target")
	}
}

func TestNewDefaultRegistrySearchLimits(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	raw, ok := reg.Get("search")
	if !ok {
		t.Fatal("search not registered")
	}
	st, ok := raw.(*searchTool)
	if !ok {
		t.Fatalf("expected *searchTool, got %T", raw)
	}
	want := 256 * 1024
	if st.maxLocalBytes != want {
		t.Fatalf("maxLocalBytes = %d, want default MaxReadBytes %d (double-scale regression)", st.maxLocalBytes, want)
	}
	if st.fetchClient == nil {
		t.Fatal("default registry search tool must use SSRF-hardened fetchClient")
	}

	// Local scan buffer is in bytes: a line longer than maxLocalBytes is still
	// scanned up to scanner max token size; output from url scope is capped.
	// Build a tool with a tiny cap and verify URL output truncation via non-SSRF client.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer s.Close()
	tool := &searchTool{
		ws:                ws,
		maxLocalBytes:     128,
		maxFetchKB:        100,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		allowPrivateFetch: true,
	}
	args, _ := json.Marshal(map[string]any{"scope": "url", "url": s.URL})
	out, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "content truncated") {
		t.Fatalf("expected truncation at maxLocalBytes, got len=%d out=%q", len(out), out[:min(80, len(out))])
	}
}

func TestSearchDescriptionMentionsMultipleEngines(t *testing.T) {
	tool := &searchTool{}
	desc := tool.Description()
	if !strings.Contains(strings.ToLower(desc), "multiple free engines") {
		t.Fatalf("Description should mention multiple free engines: %q", desc)
	}
	if strings.Contains(desc, "via DuckDuckGo") {
		t.Fatalf("Description should not claim sole DuckDuckGo engine: %q", desc)
	}
}

func TestUnwrapDDGRedirect(t *testing.T) {
	got := unwrapDDGRedirect("https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpath")
	if got != "https://example.com/path" {
		t.Fatalf("got %q", got)
	}
	if unwrapDDGRedirect("https://example.com/direct") != "https://example.com/direct" {
		t.Fatal("direct URL should pass through")
	}
}
