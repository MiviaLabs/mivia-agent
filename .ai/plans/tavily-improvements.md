# Plan: Tavily API Integration Improvements

## Summary

Upgrade the Tavily integration with full API parameter surface for search and extract,
then add comprehensive httptest-based tests for every code path.

## Changes

### 1. Extend `searchInput` struct (`searcher.go`)

Add fields that flow from the tool schema → search tool → Tavily API:

```go
type searchInput struct {
    // ... existing fields ...
    // Tavily web search parameters (scope=web only)
    SearchDepth      string   `json:"search_depth,omitempty"`       // basic, advanced, fast, ultra-fast
    Topic            string   `json:"topic,omitempty"`              // general, news, finance
    TimeRange        string   `json:"time_range,omitempty"`         // day, week, month, year
    IncludeAnswer    *bool    `json:"include_answer,omitempty"`     // true, false, "basic", "advanced"
    IncludeRawContent *bool   `json:"include_raw_content,omitempty"`// true/false
    IncludeDomains   []string `json:"include_domains,omitempty"`   // domain allow-list
    ExcludeDomains   []string `json:"exclude_domains,omitempty"`   // domain block-list
}
```

### 2. Update Param schema (`searcher.go` — `Parameters()`)

Add new properties to the schema with descriptions, types, and enums.
These params are generic to `scope=web` (ignored by free engines if Tavily not set).

### 3. Update `searchWeb()` — thread params to `searchTavily()`

```go
func (t *searchTool) searchTavily(ctx context.Context, in searchInput) (string, error)
// instead of (ctx, query, maxResults) — pass the whole searchInput struct
```

### 4. Update `searchTavily()` (`searcher_tavily.go`)

- Accept `searchInput` instead of individual params
- Build request body from all fields:
  - `SearchDepth`, `Topic`, `TimeRange`, `MaxResults`, `IncludeAnswer`, `IncludeRawContent`
  - `IncludeDomains`, `ExcludeDomains`
- Update the `tavilySearchRequest` struct with new fields
- Format output to include answer when present

### 5. Update `searchExtract()` (`searcher_tavily.go`)

- Accept `searchInput.URL` as comma-separated or single URL
- Add `ExtractDepth` param (basic/advanced)
- Add `Format` param (markdown/text)
- The `query` field from searchInput doubles as the extract reranking query

### 6. Update Tavily request/response types

- `tavilySearchRequest`: add SearchDepth enums, Topic, TimeRange, IncludeAnswer as string,
  IncludeRawContent, IncludeDomains, ExcludeDomains
- `tavilyExtractRequest`: add Format field

### 7. Integration Tests (new file `searcher_tavily_integration_test.go`)

Use httptest.Server to simulate Tavily API responses:

| # | Test | What it verifies |
|---|------|-----------------|
| 1 | `TestTavilySearchBasic` | scope=web with search_depth=basic returns formatted results |
| 2 | `TestTavilySearchAdvanced` | scope=web with search_depth=advanced + chunks_per_source |
| 3 | `TestTavilySearchAllParams` | scope=web with topic=news, time_range=week, domains |
| 4 | `TestTavilySearchIncludeAnswer` | scope=web with include_answer=true includes answer in output |
| 5 | `TestTavilySearchNoResults` | scope=web with empty results returns appropriate message |
| 6 | `TestTavilySearchHTTPError` | scope=web with HTTP 403 returns error |
| 7 | `TestTavilyExtractSingle` | scope=extract with single URL returns content |
| 8 | `TestTavilyExtractNoKey` | scope=extract without key returns error |
| 9 | `TestTavilySearchNoKey` | scope=web without key falls through to free engines |

### Order of Work

```
Step A: Update types/structs: searchInput + tavily request types
Step B: Update Parameters() schema
Step C: Refactor searchTavily() to accept searchInput
Step D: Refactor searchExtract() for multi-URL + query
Step E: Write all integration tests
Step F: Verify: go test ./internal/tools/... && go vet ./...
```
