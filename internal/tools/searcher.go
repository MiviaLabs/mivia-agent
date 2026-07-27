// Package tools implements workspace-bound agent tools.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// webSearchTool provides web search via Tavily API with free-engine fallback.
type webSearchTool struct {
	ws            *workspace.Root
	maxFetchKB    int
	httpClient    *http.Client
	webEngines    []webEngine
	tavilyKey     string
	tavilyBaseURL string
}

func (t *webSearchTool) Name() string { return "search" }
func (t *webSearchTool) Description() string {
	return "Search the web for information. Uses Tavily API when TAVILY_API_KEY is configured (with optional search_depth, topic, time_range, include_answer, include_domains, exclude_domains parameters), with fallback to free search engines."
}
func (t *webSearchTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"query": map[string]any{
			"type":        "string",
			"description": "Search query (for local/web) or path/pattern (for local)",
		},
		"max_results": map[string]any{
			"type":        "integer",
			"description": "Max results/candidates (default 15 for local, 8 for web)",
		},
		"search_depth": map[string]any{
			"type":        "string",
			"enum":        []string{"basic", "advanced", "fast", "ultra-fast"},
			"description": "Search depth for Tavily web search: basic (balanced), advanced (highest relevance), fast (low latency), ultra-fast (lowest latency). Default basic.",
		},
		"topic": map[string]any{
			"type":        "string",
			"enum":        []string{"general", "news", "finance"},
			"description": "Topic category for Tavily web search. Default general.",
		},
		"time_range": map[string]any{
			"type":        "string",
			"enum":        []string{"day", "week", "month", "year"},
			"description": "Time range filter for Tavily search results.",
		},
		"include_answer": map[string]any{
			"type":        "string",
			"enum":        []string{"basic", "advanced"},
			"description": "Include an LLM-generated answer summary in Tavily results. Omit or leave empty to skip.",
		},
		"include_domains": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "List of domains to include in Tavily web search results.",
		},
		"exclude_domains": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "List of domains to exclude from Tavily web search results.",
		},
	}, []string{"query"})
}
func (t *webSearchTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in searchInput
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Query == "" {
		return "", fmt.Errorf("query is required for web search")
	}
	return t.searchWeb(ctx, in)
}

// searchInput is the parsed argument shape for web search.
type searchInput struct {
	Query             string   `json:"query"`
	MaxResults        int      `json:"max_results"`
	SearchDepth       string   `json:"search_depth,omitempty"`
	Topic             string   `json:"topic,omitempty"`
	TimeRange         string   `json:"time_range,omitempty"`
	IncludeAnswer     string   `json:"include_answer,omitempty"`
	IncludeRawContent *bool    `json:"include_raw_content,omitempty"`
	IncludeDomains    []string `json:"include_domains,omitempty"`
	ExcludeDomains    []string `json:"exclude_domains,omitempty"`
}

// webEngine is one free web-search provider in the fallback chain.
type webEngine struct {
	name     string
	buildURL func(query string) string
	parse    func(body string, max int) []string
}

// defaultWebEngines returns the fallback chain of free web search engines.
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
