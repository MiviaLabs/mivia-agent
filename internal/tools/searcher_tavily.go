package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// --- Tavily API types ---

// tavilySearchRequest is the JSON body for POST /search.
type tavilySearchRequest struct {
	Query          string   `json:"query"`
	SearchDepth    string   `json:"search_depth,omitempty"`   // "basic" or "advanced"
	Topic          string   `json:"topic,omitempty"`          // "general" or "news"
	TimeRange      string   `json:"time_range,omitempty"`     // e.g. "day", "week", "month", "year"
	MaxResults     int      `json:"max_results,omitempty"`    // 1-10
	IncludeAnswer  string   `json:"include_answer,omitempty"` // "basic" or "advanced"
	IncludeDomains []string `json:"include_domains,omitempty"`
	ExcludeDomains []string `json:"exclude_domains,omitempty"`
}

// tavilySearchResult is one item in the results array.
type tavilySearchResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

// tavilySearchResponse is the JSON response from POST /search.
type tavilySearchResponse struct {
	Results []tavilySearchResult `json:"results"`
	Answer  string               `json:"answer,omitempty"`
}

// tavilyExtractRequest is the JSON body for POST /extract.
type tavilyExtractRequest struct {
	URLs          []string `json:"urls"`
	ExtractDepth  string   `json:"extract_depth,omitempty"` // "basic" or "advanced"
	IncludeImages bool     `json:"include_images,omitempty"`
	Format        string   `json:"format,omitempty"` // "markdown" or "text"
	Query         string   `json:"query,omitempty"`  // reranking query
}

// tavilyExtractResult is one item in the extract results array.
type tavilyExtractResult struct {
	URL        string `json:"url"`
	Content    string `json:"content"`
	RawContent string `json:"raw_content,omitempty"`
}

// tavilyExtractResponse is the JSON response from POST /extract.
type tavilyExtractResponse struct {
	Results []tavilyExtractResult `json:"results"`
}

// tavilyBase returns the Tavily API base URL (default or overridden for tests).
func (t *webSearchTool) tavilyBase() string {
	if t.tavilyBaseURL != "" {
		return t.tavilyBaseURL
	}
	return "https://api.tavily.com"
}

// tavilyAuthHeader returns the Authorization header value.
func (t *webSearchTool) tavilyAuthHeader() string {
	return "Bearer " + t.tavilyKey
}

// searchTavily performs a web search via the Tavily API.
// Returns formatted results compatible with the search tool output format.
func (t *webSearchTool) searchTavily(ctx context.Context, in searchInput) (string, error) {
	if t.tavilyKey == "" {
		return "", fmt.Errorf("tavily: API key not configured")
	}
	searchDepth := in.SearchDepth
	if searchDepth == "" {
		searchDepth = "basic"
	}
	body := tavilySearchRequest{
		Query: in.Query, SearchDepth: searchDepth, MaxResults: in.MaxResults,
		Topic: in.Topic, TimeRange: in.TimeRange, IncludeAnswer: in.IncludeAnswer,
	}
	if len(in.IncludeDomains) > 0 {
		body.IncludeDomains = in.IncludeDomains
	}
	if len(in.ExcludeDomains) > 0 {
		body.ExcludeDomains = in.ExcludeDomains
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("tavily marshal: %w", err)
	}

	u := t.tavilyBase() + "/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(payload)))
	if err != nil {
		return "", fmt.Errorf("tavily request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", t.tavilyAuthHeader())

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tavily fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("tavily: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	rawBody, err := readWebResponse(resp.Body, t.maxResultBytes, "search")
	if err != nil {
		return "", err
	}
	var result tavilySearchResponse
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return "", fmt.Errorf("tavily decode: %w", err)
	}

	if len(result.Results) == 0 {
		return "", fmt.Errorf("tavily: no results")
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Tavily search results for %q:\n", in.Query)
	for _, r := range result.Results {
		out.WriteString("\n")
		out.WriteString(formatWebResult(r.Title, r.URL, r.Content))
	}
	if result.Answer != "" {
		out.WriteString("\n\nAnswer: ")
		out.WriteString(result.Answer)
	}
	// Composition does not always shrink the body: the per-result bullet costs
	// more than an empty JSON object, and the %q query header expands. Nothing
	// is truncated here - an over-bound composition is refused outright.
	return guardWebResult(out.String(), t.maxResultBytes, "search")
}

// searchExtract performs content extraction via the Tavily /extract endpoint.
// Requires the Tavily API key to be configured.
