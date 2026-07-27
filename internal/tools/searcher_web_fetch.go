// Package tools implements workspace-bound agent tools.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (t *searchTool) searchWeb(ctx context.Context, in searchInput) (string, error) {
	if in.Query == "" {
		return "", fmt.Errorf("query is required for web search")
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 8
	}
	// Try Tavily first when key is configured (structured, high-quality results).
	if t.tavilyKey != "" {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		result, err := t.searchTavily(ctx, in.Query, in.MaxResults)
		if err == nil {
			return result, nil
		}
		// Tavily failed — fall through to free engines.
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
