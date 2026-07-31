// Package tools implements workspace-bound agent tools.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Regex patterns for free web search engine parsing.
var ddgRE = regexp.MustCompile(`(?is)<tr[^>]*>.*?</tr>`)
var linkCellRE = regexp.MustCompile(`(?is)<td[^>]*class="result-link"[^>]*>.*?<a\s+(?:[^>]*?\s+)?href="(.*?)"[^>]*>(.*?)</a>`)
var snippetCellRE = regexp.MustCompile(`(?is)<td[^>]*class="result-snippet"[^>]*>(.*?)</td>`)
var ddgHTMLLinkRE = regexp.MustCompile(`(?is)<a\b[^>]*\bclass="[^"]*\bresult__a\b[^"]*"[^>]*\bhref="([^"]*)"[^>]*>(.*?)</a>|<a\b[^>]*\bhref="([^"]*)"[^>]*\bclass="[^"]*\bresult__a\b[^"]*"[^>]*>(.*?)</a>`)
var ddgHTMLSnippetRE = regexp.MustCompile(`(?is)<a\b[^>]*\bclass="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</a>|<td\b[^>]*\bclass="[^"]*\bresult__snippet\b[^"]*"[^>]*>(.*?)</td>`)
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

// looksLikeBotChallenge detects challenge *pages*, not incidental CAPTCHA widgets.
func looksLikeBotChallenge(body string) bool {
	lower := strings.ToLower(body)
	markers := []string{
		`class="anomaly-modal"`,
		`id="challenge-form"`,
		"unfortunately, bots use duckduckgo too",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

func (t *webSearchTool) searchWeb(ctx context.Context, in searchInput) (string, error) {
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
		result, err := t.searchTavily(ctx, in)
		if err == nil {
			return result, nil
		}
		if errors.Is(err, errWebResponseBudget) {
			// A budget refusal is not a provider outage. Falling through here
			// would replace the refused result with different engines' results
			// and tell the operator nothing about the bound they need to raise.
			return "", err
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
		// The free-engine composition is guarded by the same budget the tool
		// declares. This path is reached on ANY non-budget Tavily failure and
		// is the only path a keyless install takes, so leaving it unguarded
		// would make ResultBudgetBytes() a lie.
		return guardWebResult(strings.Join(results, "\n"), t.maxResultBytes, "search")
	}
	return guardWebResult("no web results found", t.maxResultBytes, "search")
}
func (t *webSearchTool) fetchWebEngine(ctx context.Context, eng webEngine, query string, max int) ([]string, error) {
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
		// Snippets are NOT cut. This used to clip every snippet to 150 bytes,
		// which silently discarded most of each Tavily result's content and
		// made the tool's "returns what it fetched" contract false. The byte
		// guard on the composed result is the single honest bound now: over
		// budget refuses loudly rather than handing back a quiet stub.
		line += fmt.Sprintf("\n  %s", snippet)
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
