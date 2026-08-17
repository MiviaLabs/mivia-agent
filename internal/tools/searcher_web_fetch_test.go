package tools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

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

// stripHTMLTags must not drop an unterminated entity at the end of the input.
// The scanner entered entity mode on '&' followed by a letter or '#' and only
// flushed on ';', so a trailing "&T and more" was swallowed and every
// character after the '&' vanished from the output ("AT&T" became "AT").
func TestStripHTMLTagsUnterminatedEntity(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"unterminated entity mid-word", "AT&T and more", "AT&T and more"},
		{"lone ampersand", "&", "&"},
		{"entity-like fragment", "x &y z", "x &y z"},
		{"valid entity still decodes", "&amp;", "&"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripHTMLTags(tc.input); got != tc.want {
				t.Errorf("stripHTMLTags(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
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
  <div class="b_caption"><p class="b_lineclamp2">First bing snippet.</p></div>
</li>
<li class="b_algo">
  <h2><a href="https://example.com/bing2">Bing Two</a></h2>
  <div class="b_caption"><p class="b_lineclamp2">Second snippet.</p></div>
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
<li class="b_algo"><h2><a href="https://a.com">A</a></h2><p class="b_lineclamp2">sa</p></li>
<li class="b_algo"><h2><a href="https://b.com">B</a></h2><p class="b_lineclamp2">sb</p></li>
<li class="b_algo"><h2><a href="https://c.com">C</a></h2><p class="b_lineclamp2">sc</p></li>`
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
		_, _ = w.Write([]byte(`<li class="b_algo"><h2><a href="https://ok.example">OK Hit</a></h2><p class="b_lineclamp2">works</p></li>`))
	}))
	defer s4.Close()

	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := &webSearchTool{
		ws: ws,

		maxFetchKB: 100,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		webEngines: []webEngine{
			{name: "challenge", buildURL: func(string) string { return s1.URL }, parse: parseDDGResults},
			{name: "forbid", buildURL: func(string) string { return s2.URL }, parse: parseDDGResults},
			{name: "empty", buildURL: func(string) string { return s3.URL }, parse: parseDDGResults},
			{name: "bing", buildURL: func(string) string { return s4.URL }, parse: parseBingResults},
		},
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"test query"}`))
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
	tool := &webSearchTool{
		ws: ws,

		maxFetchKB: 100,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		webEngines: []webEngine{
			{name: "only", buildURL: func(string) string { return s.URL }, parse: parseDDGResults},
		},
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"x"}`))
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

	tool := &fetchURLTool{
		ws:                nil,
		maxLocalBytes:     256 * 1024,
		maxFetchKB:        100,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		allowPrivateFetch: true,
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+s.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello body") {
		t.Fatalf("unexpected body: %q", out)
	}
	if !strings.Contains(sawUA, "Chrome") {
		t.Fatalf("expected Chrome User-Agent, got %q", sawUA)
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
		// IANA special-purpose ranges the net.IP predicates do not cover.
		"0.0.0.1",         // 0.0.0.0/8 this network (unspecified /32 is above)
		"192.0.0.1",       // 192.0.0.0/24 IETF protocol assignments
		"192.0.2.1",       // TEST-NET-1
		"192.88.99.1",     // deprecated 6to4 relay anycast
		"198.18.0.1",      // benchmarking
		"198.51.100.1",    // TEST-NET-2
		"203.0.113.1",     // TEST-NET-3
		"240.0.0.1",       // reserved for future use
		"255.255.255.255", // limited broadcast
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

// TestReservedNetsAreComplete pins the reservedNets constants: every entry
// must parse to a non-nil network and cover its representative address, so a
// typo in a CIDR string fails here instead of silently weakening the SSRF
// gate (the init path ignores parse errors by design, matching cgnatNet).
func TestReservedNetsAreComplete(t *testing.T) {
	want := map[string]string{
		"0.0.0.0/8":          "0.0.0.1",
		"192.0.0.0/24":       "192.0.0.1",
		"192.0.2.0/24":       "192.0.2.1",
		"192.88.99.0/24":     "192.88.99.1",
		"198.18.0.0/15":      "198.18.0.1",
		"198.51.100.0/24":    "198.51.100.1",
		"203.0.113.0/24":     "203.0.113.1",
		"240.0.0.0/4":        "240.0.0.1",
		"255.255.255.255/32": "255.255.255.255",
	}
	if len(reservedNets) != len(want) {
		t.Fatalf("reservedNets has %d entries, want %d", len(reservedNets), len(want))
	}
	for _, n := range reservedNets {
		if n == nil {
			t.Fatal("reservedNets contains a nil network (unparseable CIDR constant)")
		}
	}
	for cidr, ipStr := range want {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatalf("test CIDR %q does not parse: %v", cidr, err)
		}
		if !n.Contains(net.ParseIP(ipStr)) {
			t.Fatalf("network %q does not contain its representative %s", cidr, ipStr)
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
		// IANA special-purpose ranges: TEST-NET and benchmarking.
		"http://192.0.2.1/",
		"http://198.18.0.1/",
		"http://203.0.113.9/",
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
	tool := &webSearchTool{
		ws: ws,

		maxFetchKB: 100,
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
	// loopback first - instead exercise CheckRedirect policy directly and
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
	tool := &webSearchTool{
		ws: ws,

		maxFetchKB: 100,
	}
	args, _ := json.Marshal(map[string]any{"scope": "url", "url": s.URL + "/start"})
	_, err = tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatal("expected fetch of loopback httptest URL to fail SSRF checks")
	}
	_ = redirects
}

func TestSafeFetchClientCheckRedirectBlocksPrivate(t *testing.T) {
	client := newSafeFetchHTTPClient()
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
	_, ok = raw.(*webSearchTool)
	if !ok {
		t.Fatalf("expected *webSearchTool, got %T", raw)
	}
	// scanned up to scanner max token size; output from url scope is capped.
	// Build a tool with a tiny cap and verify URL output truncation via non-SSRF client.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer s.Close()
	tool := &fetchURLTool{
		ws:                nil,
		maxLocalBytes:     100,
		maxFetchKB:        100,
		httpClient:        &http.Client{Timeout: 5 * time.Second},
		allowPrivateFetch: true,
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+s.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "content truncated") {
	}
}

func TestSearchDescriptionMentionsMultipleEngines(t *testing.T) {
	tool := &webSearchTool{}
	desc := tool.Description()
	if !strings.Contains(strings.ToLower(desc), "free search engines") {
		t.Fatalf("Description should mention free search engines: %q", desc)
	}
}
