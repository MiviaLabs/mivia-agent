// Package tools implements workspace-bound agent tools.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// searchTool provides unified search across local files, web, and URLs.
// Uses only stdlib — no external dependencies.
type searchTool struct {
	ws            *workspace.Root
	maxLocalBytes int // local scan buffer + URL output cap (bytes)
	maxFetchKB    int // HTTP response body read limit (KiB)
	httpClient    *http.Client
	// fetchClient is used for scope=url (SSRF-hardened). Falls back to httpClient.
	fetchClient *http.Client
	// allowPrivateFetch disables SSRF checks (tests only; httptest is loopback).
	allowPrivateFetch bool
	// webEngines overrides the default multi-provider chain (tests inject httptest URLs).
	webEngines []webEngine
}

// webEngine is one free web-search provider in the fallback chain.
type webEngine struct {
	name     string
	buildURL func(query string) string
	parse    func(body string, max int) []string
}

func (t *searchTool) Name() string { return "search" }
func (t *searchTool) Description() string {
	return "Unified search: scope=local (grep & glob files), scope=web (web search via multiple free engines, no API key), scope=url (fetch and read URL contents). All return text results."
}
func (t *searchTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"scope": map[string]any{
			"type":        "string",
			"description": "'local' for file search, 'web' for internet search, 'url' to fetch a URL",
			"enum":        []string{"local", "web", "url"},
		},
		"query": map[string]any{
			"type":        "string",
			"description": "Search query (for local/web) or path/pattern (for local)",
		},
		"path": map[string]any{
			"type":        "string",
			"description": "Directory to search in (local scope only, default '.')",
		},
		"glob": map[string]any{
			"type":        "string",
			"description": "Filename glob filter (local scope only, e.g. *.py, **/*.md)",
		},
		"url": map[string]any{
			"type":        "string",
			"description": "URL to fetch (url scope only)",
		},
		"max_results": map[string]any{
			"type":        "integer",
			"description": "Max results/candidates (default 15 for local, 8 for web)",
		},
	}, []string{"scope"})
}

var ddgRE = regexp.MustCompile(`(?is)<tr[^>]*>.*?</tr>`)
var linkCellRE = regexp.MustCompile(`(?is)<td[^>]*class="result-link"[^>]*>.*?<a\s+(?:[^>]*?\s+)?href="(.*?)"[^>]*>(.*?)</a>`)
var snippetCellRE = regexp.MustCompile(`(?is)<td[^>]*class="result-snippet"[^>]*>(.*?)</td>`)

// DuckDuckGo HTML (html.duckduckgo.com): class may appear before or after href.
var ddgHTMLLinkRE = regexp.MustCompile(`(?is)<a\b[^>]*\bclass="[^"]*\bresult__a\b[^"]*"[^>]*\bhref="([^"]*)"[^>]*>(.*?)</a>|<a\b[^>]*\bhref="([^"]*)"[^>]*\bclass="[^"]*\bresult__a\b[^"]*"[^>]*>(.*?)</a>`)
var ddgHTMLSnippetRE = regexp.MustCompile(`(?is)<a\b[^>]*\bclass="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</a>|<td\b[^>]*\bclass="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</td>`)

// Bing SERP: li.b_algo blocks with h2>a and snippet in p.b_lineclamp2.
var bingAlgoRE = regexp.MustCompile(`(?is)<li\b[^>]*\bclass="[^"]*\bb_algo\b[^"]*"[^>]*>(.*?)</li>`)
var bingLinkRE = regexp.MustCompile(`(?is)<h2[^>]*>\s*<a\b[^>]*\bhref="([^"]+)"[^>]*>(.*?)</a>`)
var bingSnippetRE = regexp.MustCompile(`(?is)<p[^>]*class="[^"]*\bb_lineclamp2\b[^"]*"[^>]*>(.*?)</p>`)
var bingSnippetFallbackRE = regexp.MustCompile(`(?is)<p>(.*?)</p>`)

const browserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

func setBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", browserUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
}

// looksLikeBotChallenge detects challenge *pages*, not incidental captcha-related
// strings (e.g. recaptcha scripts embedded in normal SERPs).
func looksLikeBotChallenge(body string) bool {
	lower := strings.ToLower(body)
	for _, marker := range []string{
		"anomaly-modal",
		`id="challenge-form"`,
		"unfortunately, bots use duckduckgo too",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func defaultWebEngines() []webEngine {
	return []webEngine{
		{
			name: "ddg-lite",
			buildURL: func(q string) string {
				return "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(q)
			},
			parse: parseDDGResults,
		},
		{
			name: "ddg-html",
			buildURL: func(q string) string {
				return "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(q)
			},
			parse: parseDDGHTMLResults,
		},
		{
			name: "ddg-html-root",
			buildURL: func(q string) string {
				return "https://duckduckgo.com/html/?q=" + url.QueryEscape(q)
			},
			parse: parseDDGHTMLResults,
		},
		{
			name: "bing",
			buildURL: func(q string) string {
				return "https://www.bing.com/search?q=" + url.QueryEscape(q)
			},
			parse: parseBingResults,
		},
		{
			name: "ddg-ia",
			buildURL: func(q string) string {
				return "https://api.duckduckgo.com/?q=" + url.QueryEscape(q) + "&format=json&no_redirect=1&no_html=1"
			},
			parse: parseDDGIAJSON,
		},
	}
}

// searchInput is the parsed argument shape for all three search scopes.
type searchInput struct {
	Scope      string `json:"scope"`
	Query      string `json:"query"`
	Path       string `json:"path"`
	Glob       string `json:"glob"`
	URL        string `json:"url"`
	MaxResults int    `json:"max_results"`
}

func (t *searchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in searchInput
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}

	switch in.Scope {
	case "local":
		return t.searchLocal(ctx, in)
	case "web":
		return t.searchWeb(ctx, in)
	case "url":
		return t.fetchURL(ctx, in)
	default:
		return "", fmt.Errorf("invalid scope %q: must be local, web, or url", in.Scope)
	}
}

// --- Local search ---

func (t *searchTool) searchLocal(ctx context.Context, in searchInput) (string, error) {
	if in.Query == "" {
		return "", fmt.Errorf("query is required for local search")
	}
	if in.Path == "" {
		in.Path = "."
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 15
	}

	q := strings.ToLower(in.Query)

	root, err := t.ws.Resolve(in.Path)
	if err != nil {
		return "", err
	}

	var results []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		// Skip symlinks to avoid workspace escape.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		// Early exit if we have enough results.
		if len(results) >= in.MaxResults {
			return fmt.Errorf("max results")
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		// Glob filter on filename.
		if in.Glob != "" {
			ok, _ := filepath.Match(in.Glob, d.Name())
			if !ok {
				return nil
			}
		}
		rel := t.ws.Rel(path)
		if isSecretPath(rel) {
			return nil
		}

		// Check if filename matches first (fast path).
		if strings.Contains(strings.ToLower(d.Name()), q) {
			results = append(results, fmt.Sprintf("%s (filename match)", rel))
			if len(results) >= in.MaxResults {
				return fmt.Errorf("max results")
			}
		}

		// Check content (slower).
		f, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer f.Close()

		// Read first 8KB to check for binary.
		header := make([]byte, 8192)
		n, _ := f.Read(header)
		if !utf8.Valid(header[:n]) {
			return nil // skip binary
		}

		// Reset and read line by line.
		f.Seek(0, 0)
		sc := bufio.NewScanner(f)
		maxBuf := t.maxLocalBytes
		if maxBuf <= 0 {
			maxBuf = 256 * 1024
		}
		buf := make([]byte, maxBuf)
		sc.Buffer(buf, maxBuf)

		lineNo := 0
		for sc.Scan() {
			lineNo++
			line := sc.Text()
			if strings.Contains(strings.ToLower(line), q) {
				if len(line) > 200 {
					line = line[:200] + "..."
				}
				results = append(results, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
				if len(results) >= in.MaxResults {
					return fmt.Errorf("max results")
				}
			}
		}
		return nil
	})
	if err != nil {
		// "max results" is our sentinel to stop walking — it's not an error.
		if err.Error() != "max results" && err != context.Canceled {
			return "", err
		}
	}
	if len(results) == 0 {
		return "no matches found", nil
	}
	out := strings.Join(results, "\n")
	if len(results) >= in.MaxResults {
		out += fmt.Sprintf("\n... truncated at %d results", in.MaxResults)
	}
	return out, nil
}

// --- Web search (multi-provider fallback) ---

func (t *searchTool) searchWeb(ctx context.Context, in searchInput) (string, error) {
	if in.Query == "" {
		return "", fmt.Errorf("query is required for web search")
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 8
	}

	engines := t.webEngines
	if engines == nil {
		engines = defaultWebEngines()
	}

	// Add politeness delay between fallback attempts (only for default engine chain).
	useDelay := t.webEngines == nil

	for i, eng := range engines {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		if i > 0 && useDelay {
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
		results, err := t.fetchWebEngine(ctx, eng, in.Query, in.MaxResults)
		if err != nil || len(results) == 0 {
			// Challenge, non-2xx, transport error, or empty parse → try next engine.
			continue
		}
		return strings.Join(results, "\n"), nil
	}
	return "no web results found", nil
}

func (t *searchTool) fetchWebEngine(ctx context.Context, eng webEngine, query string, max int) ([]string, error) {
	u := eng.buildURL(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%s request: %w", eng.name, err)
	}
	setBrowserHeaders(req)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s fetch: %w", eng.name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(t.maxFetchKB)*1024))
	if err != nil {
		return nil, fmt.Errorf("%s read: %w", eng.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s status %d", eng.name, resp.StatusCode)
	}
	raw := string(body)
	if looksLikeBotChallenge(raw) {
		return nil, fmt.Errorf("%s bot challenge", eng.name)
	}
	if eng.parse == nil {
		return nil, nil
	}
	return eng.parse(raw, max), nil
}

func formatWebResult(title, href, snippet string) string {
	title = strings.TrimSpace(title)
	href = strings.TrimSpace(href)
	snippet = strings.TrimSpace(snippet)
	line := fmt.Sprintf("• %s", title)
	if href != "" {
		line += fmt.Sprintf("\n  %s", href)
	}
	if snippet != "" {
		line += fmt.Sprintf("\n  %s", truncateUTF8(snippet, 150))
	}
	return line
}

// parseDDGResults extracts search results from DuckDuckGo Lite HTML.
// The Lite page uses a simple table with alternating result-link and result-snippet rows.
func parseDDGResults(html string, max int) []string {
	var out []string
	rows := ddgRE.FindAllString(html, -1)
	var pendingTitle, pendingURL string

	for _, row := range rows {
		if m := linkCellRE.FindStringSubmatch(row); len(m) >= 3 {
			href := unwrapDDGRedirect(decodeHTMLEntities(strings.TrimSpace(m[1])))
			title := stripHTMLTags(strings.TrimSpace(m[2]))
			if pendingTitle == "" {
				pendingURL = href
				pendingTitle = title
			}
		}
		if m := snippetCellRE.FindStringSubmatch(row); len(m) >= 2 {
			snippet := stripHTMLTags(strings.TrimSpace(m[1]))
			if pendingTitle != "" {
				out = append(out, formatWebResult(pendingTitle, pendingURL, snippet))
				pendingTitle = ""
				pendingURL = ""
				if len(out) >= max {
					break
				}
			}
		}
	}
	return out
}

// parseDDGHTMLResults extracts results from DuckDuckGo HTML SERP (result__a links).
func parseDDGHTMLResults(html string, max int) []string {
	matches := ddgHTMLLinkRE.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return nil
	}
	snippets := ddgHTMLSnippetRE.FindAllStringSubmatch(html, -1)
	var out []string
	for i, m := range matches {
		href, title := "", ""
		if m[1] != "" {
			href = m[1]
			title = m[2]
		} else {
			href = m[3]
			title = m[4]
		}
		href = unwrapDDGRedirect(decodeHTMLEntities(strings.TrimSpace(href)))
		title = stripHTMLTags(strings.TrimSpace(title))
		if title == "" && href == "" {
			continue
		}
		snippet := ""
		if i < len(snippets) {
			if snippets[i][1] != "" {
				snippet = snippets[i][1]
			} else {
				snippet = snippets[i][2]
			}
			snippet = stripHTMLTags(strings.TrimSpace(snippet))
		}
		out = append(out, formatWebResult(title, href, snippet))
		if len(out) >= max {
			break
		}
	}
	return out
}

// unwrapDDGRedirect extracts the destination from DDG redirect wrappers (/l/?uddg=...).
func unwrapDDGRedirect(href string) string {
	href = strings.TrimSpace(href)
	if href == "" {
		return href
	}
	// Protocol-relative
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if uddg := u.Query().Get("uddg"); uddg != "" {
		if decoded, err := url.QueryUnescape(uddg); err == nil && decoded != "" {
			return decoded
		}
	}
	return href
}

// parseBingResults extracts results from Bing HTML SERP (li.b_algo).
func parseBingResults(html string, max int) []string {
	blocks := bingAlgoRE.FindAllStringSubmatch(html, -1)
	var out []string
	for _, block := range blocks {
		inner := block[1]
		m := bingLinkRE.FindStringSubmatch(inner)
		if len(m) < 3 {
			continue
		}
		href := decodeHTMLEntities(strings.TrimSpace(m[1]))
		title := stripHTMLTags(strings.TrimSpace(m[2]))
		snippet := ""
		if sm := bingSnippetRE.FindStringSubmatch(inner); len(sm) >= 2 {
			snippet = stripHTMLTags(strings.TrimSpace(sm[1]))
		} else if sm := bingSnippetFallbackRE.FindStringSubmatch(inner); len(sm) >= 2 {
			snippet = stripHTMLTags(strings.TrimSpace(sm[1]))
		}
		if title == "" && href == "" {
			continue
		}
		out = append(out, formatWebResult(title, href, snippet))
		if len(out) >= max {
			break
		}
	}
	return out
}

// parseDDGIAJSON extracts results from DuckDuckGo Instant Answer JSON API.
// Weak coverage (not a full SERP) but free and non-HTML.
func parseDDGIAJSON(body string, max int) []string {
	var payload struct {
		Heading        string `json:"Heading"`
		AbstractText   string `json:"AbstractText"`
		AbstractURL    string `json:"AbstractURL"`
		AbstractSource string `json:"AbstractSource"`
		Results        []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"Results"`
		RelatedTopics []json.RawMessage `json:"RelatedTopics"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil
	}

	var out []string
	if payload.AbstractText != "" {
		title := payload.Heading
		if title == "" {
			title = payload.AbstractSource
		}
		if title == "" {
			title = "Abstract"
		}
		out = append(out, formatWebResult(title, payload.AbstractURL, payload.AbstractText))
	}
	for _, r := range payload.Results {
		if len(out) >= max {
			break
		}
		if r.Text == "" && r.FirstURL == "" {
			continue
		}
		out = append(out, formatWebResult(r.Text, r.FirstURL, ""))
	}
	// RelatedTopics entries may be flat {Text,FirstURL} or nested {Name,Topics:[...]}.
	var walkRelated func(items []json.RawMessage)
	walkRelated = func(items []json.RawMessage) {
		for _, raw := range items {
			if len(out) >= max {
				return
			}
			var flat struct {
				Text     string `json:"Text"`
				FirstURL string `json:"FirstURL"`
			}
			if err := json.Unmarshal(raw, &flat); err == nil && (flat.Text != "" || flat.FirstURL != "") {
				out = append(out, formatWebResult(flat.Text, flat.FirstURL, ""))
				continue
			}
			var nested struct {
				Topics []json.RawMessage `json:"Topics"`
			}
			if err := json.Unmarshal(raw, &nested); err == nil && len(nested.Topics) > 0 {
				walkRelated(nested.Topics)
			}
		}
	}
	walkRelated(payload.RelatedTopics)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// --- URL fetch (SSRF-hardened) ---

const maxFetchRedirects = 5

// cgnatNet is RFC 6598 shared address space (100.64.0.0/10).
var cgnatNet = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// isBlockedFetchIP reports whether ip must not be contacted for scope=url.
func isBlockedFetchIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsMulticast() {
		return true
	}
	if cgnatNet != nil && cgnatNet.Contains(ip) {
		return true
	}
	return false
}

// validateFetchURL enforces http(s) and rejects hosts that resolve (or are)
// private / reserved addresses. Fail-closed on DNS failure.
func validateFetchURL(ctx context.Context, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid URL: must be http or https")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("invalid URL: must be http or https")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("invalid URL: missing host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedFetchIP(ip) {
			return fmt.Errorf("blocked URL: private or reserved address")
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("blocked URL: hostname resolution failed: %w", err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("blocked URL: hostname resolution failed: no addresses")
	}
	for _, a := range addrs {
		if isBlockedFetchIP(a.IP) {
			return fmt.Errorf("blocked URL: private or reserved address")
		}
	}
	return nil
}

// newSafeFetchHTTPClient builds a client that re-validates redirect targets and
// refuses dials to blocked IPs (defense in depth for scope=url).
func newSafeFetchHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	baseDialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if ip := net.ParseIP(host); ip != nil {
				if isBlockedFetchIP(ip) {
					return nil, fmt.Errorf("blocked dial: private or reserved address")
				}
				return baseDialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("blocked dial: resolve failed: %w", err)
			}
			var firstErr error
			for _, a := range ips {
				if isBlockedFetchIP(a.IP) {
					if firstErr == nil {
						firstErr = fmt.Errorf("blocked dial: private or reserved address")
					}
					continue
				}
				conn, dialErr := baseDialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				firstErr = dialErr
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("blocked dial: no allowed addresses")
			}
			return nil, firstErr
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxFetchRedirects {
				return fmt.Errorf("stopped after %d redirects", maxFetchRedirects)
			}
			if err := validateFetchURL(req.Context(), req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

func (t *searchTool) urlHTTPClient() *http.Client {
	if t.fetchClient != nil {
		return t.fetchClient
	}
	return t.httpClient
}

func (t *searchTool) fetchURL(ctx context.Context, in searchInput) (string, error) {
	if in.URL == "" {
		return "", fmt.Errorf("url is required for url scope")
	}

	if !t.allowPrivateFetch {
		if err := validateFetchURL(ctx, in.URL); err != nil {
			return "", err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, in.URL, nil)
	if err != nil {
		return "", fmt.Errorf("fetch request: %w", err)
	}
	setBrowserHeaders(req)

	client := t.urlHTTPClient()
	if client == nil {
		if t.allowPrivateFetch {
			client = &http.Client{Timeout: 15 * time.Second}
		} else {
			client = newSafeFetchHTTPClient(15 * time.Second)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	// Reject non-text content types.
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !isTextContentType(ct) {
		return "", fmt.Errorf("skipped non-text content: %s", ct)
	}

	maxFetch := t.maxFetchKB
	if maxFetch <= 0 {
		maxFetch = 100
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxFetch)*1024))
	if err != nil {
		return "", fmt.Errorf("fetch read: %w", err)
	}

	text := stripHTMLTags(string(body))
	text = strings.TrimSpace(text)
	if len(text) == 0 {
		return "(empty page)", nil
	}
	// Cap output at maxLocalBytes.
	maxOut := t.maxLocalBytes
	if maxOut <= 0 {
		maxOut = 256 * 1024
	}
	if len(text) > maxOut {
		text = truncateUTF8(text, maxOut) + "\n... (content truncated)"
	}
	return fmt.Sprintf("URL: %s\nStatus: %s\n\n%s", in.URL, resp.Status, text), nil
}
