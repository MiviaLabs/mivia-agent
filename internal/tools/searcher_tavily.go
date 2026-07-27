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
	Query         string `json:"query"`
	SearchDepth   string `json:"search_depth,omitempty"`   // "basic" or "advanced"
	Topic         string `json:"topic,omitempty"`          // "general" or "news"
	MaxResults    int    `json:"max_results,omitempty"`    // 1-10
	IncludeAnswer bool   `json:"include_answer,omitempty"` // include AI answer summary
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
func (t *searchTool) tavilyBase() string {
	if t.tavilyBaseURL != "" {
		return t.tavilyBaseURL
	}
	return "https://api.tavily.com"
}

// tavilyAuthHeader returns the Authorization header value.
func (t *searchTool) tavilyAuthHeader() string {
	return "Bearer " + t.tavilyKey
}

// searchTavily performs a web search via the Tavily API.
// Returns formatted results compatible with the search tool output format.
func (t *searchTool) searchTavily(ctx context.Context, query string, maxResults int) (string, error) {
	if t.tavilyKey == "" {
		return "", fmt.Errorf("tavily: API key not configured")
	}

	body := tavilySearchRequest{
		Query:         query,
		SearchDepth:   "basic",
		MaxResults:    maxResults,
		IncludeAnswer: false,
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

	var result tavilySearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("tavily decode: %w", err)
	}

	if len(result.Results) == 0 {
		return "", fmt.Errorf("tavily: no results")
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Tavily search results for %q:\n", query)
	for _, r := range result.Results {
		out.WriteString("\n")
		out.WriteString(formatWebResult(r.Title, r.URL, r.Content))
	}
	return out.String(), nil
}

// searchExtract performs content extraction via the Tavily /extract endpoint.
// Requires the Tavily API key to be configured.
func (t *searchTool) searchExtract(ctx context.Context, in searchInput) (string, error) {
	if t.tavilyKey == "" {
		return "", fmt.Errorf("extract requires TAVILY_API_KEY to be set")
	}
	if in.URL == "" {
		return "", fmt.Errorf("url is required for extract scope")
	}

	body := tavilyExtractRequest{
		URLs:         []string{in.URL},
		ExtractDepth: "basic",
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("tavily extract marshal: %w", err)
	}

	u := t.tavilyBase() + "/extract"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(payload)))
	if err != nil {
		return "", fmt.Errorf("tavily extract request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", t.tavilyAuthHeader())

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tavily extract fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("tavily extract: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	var result tavilyExtractResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("tavily extract decode: %w", err)
	}

	if len(result.Results) == 0 {
		return "", fmt.Errorf("tavily extract: no results for %s", in.URL)
	}

	r := result.Results[0]
	content := r.Content
	if content == "" {
		content = r.RawContent
	}
	if content == "" {
		return fmt.Sprintf("Tavily extracted: %s\n(empty content)", in.URL), nil
	}
	return fmt.Sprintf("Tavily extract: %s\n\n%s", in.URL, content), nil
}
