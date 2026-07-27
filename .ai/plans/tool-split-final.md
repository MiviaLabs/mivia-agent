# Final Implementation Plan: Split `search` into `search`, `extract`, `fetch_url`

## Summary
Split the monolithic `search` tool (scope=local, web, url, extract) into three focused tools. `search` becomes web-only, `extract` handles Tavily content extraction, and `fetch_url` handles SSRF-safe URL fetching.

## File Changes

### Create new files

| File | Contents |
|------|----------|
| `internal/tools/fetch_url.go` | Standalone `fetchURLTool` struct, `fetchURL()` method, SSRF validation, `validateFetchURL()`, `isBlockedFetchIP()`, `newSafeFetchHTTPClient()`, `isTextContentType()` (moved from searcher_web_fetch.go) |
| `internal/tools/extract.go` | Standalone `extractTool` struct, `searchExtract()` → `extractContent()`, Tavily response types, `tavilyBase()`, `tavilyAuthHeader()` (moved from searcher_tavily.go) |

### Modify existing files

| File | Changes |
|------|---------|
| `internal/tools/searcher.go` | Rename to `webSearchTool`, strip `scope=local`/`url`/`extract` from Execute dispatch. Remove `searchInput` struct fields not used by web search. Simplify Parameters() to only web-search fields. Simplify Description(). |
| `internal/tools/searcher_web_fetch.go` | Keep shared utilities: `formatWebResult()`, `setBrowserHeaders()`, `stripHTMLTags()`, `truncateUTF8()`, `decodeHTMLEntities()`, `unwrapDDGRedirect()`, `looksLikeBotChallenge()`, `defaultWebEngines()`, parse functions, regex vars. Remove `fetchURL()`, `validateFetchURL()`, `isBlockedFetchIP()`, `newSafeFetchHTTPClient()`, `isTextContentType()` (moved to fetch_url.go). |
| `internal/tools/searcher_tavily.go` | Delete or gut — Tavily search moves to `webSearchTool` (searcher.go), Tavily extract moves to `extractTool` (extract.go). |
| `internal/tools/tools.go` | Register `fetchURLTool`, `extractTool` in `NewDefaultRegistry()`. Update special-case schema validation. Remove `scope=url`/`scope=extract` validation. |
| `internal/tools/searcher_test.go` | Update `TestSearchOpenAISchema` (expect fewer params). Update `TestSearchToolRegistered`. Add tests for `fetch_url`. |
| `internal/tools/searcher_tavily_integration_test.go` | Split tests: Tavily search tests stay with `search` tool, extract tests move to `extract` tool file. |
| `internal/cli/tui_keys.go` | Keep `/search` slash command as-is (still triggers web search). |
| `internal/cli/chat.go` | Keep `/search` slash command as-is. |
| `internal/cli/tool_verbs.go` | Add cases for `"extract"` and `"fetch_url"`. |
| `internal/subagents/prompts.go` | Update tool list to mention `search`, `extract`, `fetch_url`. |

### Delete / gut

| File | Action |
|------|--------|
| `internal/tools/searcher_tavily.go` | Gut all Tavily types + extract code — they go into extract.go. Keep only `searchTavily()` (moves to searcher.go). Then delete file. |

## Tool Contracts

### `search` (web search)
```
Name: search
Description: Search the web for information. Uses Tavily API when TAVILY_API_KEY
  is configured, with fallback to free search engines.
Parameters: query (required), max_results, search_depth, topic, time_range,
  include_answer, include_domains, exclude_domains
```

### `fetch_url` (URL fetch)
```
Name: fetch_url
Description: Fetch and read the contents of a URL. Uses SSRF protection to
  block private/internal addresses. Prefer over run_command for reading URLs.
Parameters: url (required)
```

### `extract` (Tavily content extraction)
```
Name: extract
Description: Extract content from a URL using Tavily. Requires TAVILY_API_KEY
  to be configured. Supports structured content extraction with optional
  reranking query.
Parameters: url (required), query, extract_depth, format
```

## Implementation Order
1. Create `fetch_url.go` — standalone tool (easiest, fully independent)
2. Create `extract.go` — standalone tool (independent)
3. Rewrite `searcher.go` — strip to web-only, remove unnecessary code
4. Update `tools.go` — register new tools
5. Update validation + test files
6. Update CLI references
7. Run full test suite, fix any failures
