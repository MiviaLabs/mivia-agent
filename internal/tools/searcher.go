// Package tools implements workspace-bound agent tools.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

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
	// tavilyKey is the Tavily API key. When set, Tavily is tried first for web search.
	tavilyKey string
	// tavilyBaseURL overrides the Tavily API endpoint (tests inject httptest).
	tavilyBaseURL string
}

// webEngine is one free web-search provider in the fallback chain.
type webEngine struct {
	name     string
	buildURL func(query string) string
	parse    func(body string, max int) []string
}

func (t *searchTool) Name() string { return "search" }
func (t *searchTool) Description() string {
	return "Unified search: scope=local (grep & glob files), scope=web (web search via multiple free engines, no API key), scope=url (fetch and read URL contents), scope=extract (Tavily content extraction from URLs). All return text results."
}
func (t *searchTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"scope": map[string]any{
			"type":        "string",
			"description": "'local' for file search, 'web' for internet search, 'url' to fetch a URL, 'extract' to extract content from a URL via Tavily",
			"enum":        []string{"local", "web", "url", "extract"},
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
			"description": "URL to fetch (url scope only) or extract (extract scope only)",
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
	case "extract":
		return t.searchExtract(ctx, in)
	default:
		return "", fmt.Errorf("invalid scope %q: must be local, web, url, or extract", in.Scope)
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
	results, err := t.walkLocal(ctx, in, q)
	if err != nil {
		// "max results" is our sentinel to stop walking — it's not an error.
		if !errors.Is(err, errMaxResults) && err != context.Canceled {
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
