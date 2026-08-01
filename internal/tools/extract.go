package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// extractTool extracts content from URLs via the Tavily API.
type extractTool struct {
	tavilyKey     string
	tavilyBaseURL string
	httpClient    *http.Client
	// maxResultBytes is the byte bound this tool both enforces and declares.
	// It is not a truncation cap - extracted content is returned whole. See
	// web_response_budget.go.
	maxResultBytes int
}

// ResultBudgetBytes declares the bound on this tool's result for dispatcher
// output-backstop derivation (see tools.ResultBudgetTool). The tool enforces
// it on both the wire read and the composed result, so the declaration is
// exact rather than exact-modulo-framing.
func (t *extractTool) ResultBudgetBytes() int { return resolveWebResponseBudget(t.maxResultBytes) }

func (t *extractTool) Name() string { return "extract" }
func (t *extractTool) Description() string {
	return "Extract content from a URL using Tavily. Requires TAVILY_API_KEY to be configured. Supports structured content extraction with optional reranking query."
}
func (t *extractTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"url": map[string]any{
			"type":        "string",
			"description": "URL to extract content from. Several URLs may be given as a comma-separated list; every one is fetched and billed, and the content of each is returned.",
		},
		"query": map[string]any{
			"type":        "string",
			"description": "User intent for reranking extracted content chunks",
		},
		"extract_depth": map[string]any{
			"type":        "string",
			"enum":        []string{"basic", "advanced"},
			"description": "Extraction depth: basic (1 credit per 5 URLs) or advanced (2 credits per 5 URLs). Default basic.",
		},
		"format": map[string]any{
			"type":        "string",
			"enum":        []string{"markdown", "text"},
			"description": "Output format: markdown or text. Default markdown.",
		},
	}, []string{"url"})
}
func (t *extractTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	if t.tavilyKey == "" {
		return "", fmt.Errorf("extract requires TAVILY_API_KEY to be set")
	}
	var in struct {
		URL          string `json:"url"`
		Query        string `json:"query,omitempty"`
		ExtractDepth string `json:"extract_depth,omitempty"`
		Format       string `json:"format,omitempty"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.URL == "" {
		return "", fmt.Errorf("url is required for extract")
	}
	return t.extractContent(ctx, in.URL, in.Query, in.ExtractDepth, in.Format)
}

func (t *extractTool) extractContent(ctx context.Context, rawURL string, query string, extractDepth string, format string) (string, error) {
	// Parse URLs: single URL or comma-separated list.
	var urls []string
	for _, u := range strings.Split(rawURL, ",") {
		if s := strings.TrimSpace(u); s != "" {
			urls = append(urls, s)
		}
	}
	if len(urls) == 0 {
		return "", fmt.Errorf("url is required for extract")
	}
	if extractDepth == "" {
		extractDepth = "basic"
	}
	if format == "" {
		format = "markdown"
	}
	body := tavilyExtractRequest{
		URLs: urls, ExtractDepth: extractDepth, Format: format,
		Query: query,
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
	rawBody, err := readWebResponse(resp.Body, t.maxResultBytes, "extract")
	if err != nil {
		return "", err
	}
	var result tavilyExtractResponse
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return "", fmt.Errorf("tavily extract decode: %w", err)
	}
	if len(result.Results) == 0 {
		return "", fmt.Errorf("tavily extract: no results for %s", rawURL)
	}
	// Extracted content is returned WHOLE. The guard refuses an over-bound
	// result rather than cutting it; the URL echo rides on top of the content,
	// so a result within a few dozen bytes of the bound can be refused for the
	// framing alone, and the refusal says which key raises the bound.
	return guardWebResult(formatExtractResults(result.Results, rawURL, len(urls)), t.maxResultBytes, "extract")
}

// formatExtractResults composes every returned result, not just the first: the
// url argument is explicitly a comma-separated list, so returning Results[0]
// alone silently dropped content the caller requested and the provider had
// already billed for. requested is how many URLs were asked for, used to report
// a short provider return rather than letting a partial answer read as full
// coverage. The echoed URL prefers the provider's, so the model-supplied
// argument - which is bounded by the request, not the response budget - only
// reaches the output when the provider omits its own.
func formatExtractResults(results []tavilyExtractResult, rawURL string, requested int) string {
	sections := make([]string, 0, len(results))
	for _, r := range results {
		target := strings.TrimSpace(r.URL)
		if target == "" {
			target = rawURL
		}
		content := r.Content
		if content == "" {
			content = r.RawContent
		}
		if content == "" {
			sections = append(sections, fmt.Sprintf("Tavily extracted: %s\n(empty content)", target))
			continue
		}
		sections = append(sections, fmt.Sprintf("Tavily extract: %s\n\n%s", target, content))
	}
	out := strings.Join(sections, "\n\n")
	if len(results) < requested {
		out += fmt.Sprintf("\n\n(%d of %d requested URLs returned content)", len(results), requested)
	}
	return out
}

func (t *extractTool) tavilyBase() string {
	if t.tavilyBaseURL != "" {
		return t.tavilyBaseURL
	}
	return "https://api.tavily.com"
}

func (t *extractTool) tavilyAuthHeader() string {
	return "Bearer " + t.tavilyKey
}

// Ensure extractTool implements required interfaces.
var _ Tool = (*extractTool)(nil)
