# Plan: ZAI / GLM Provider Adapter

**Status:** ⏸ Deferred — blocked by PROVIDER-ARCHITECTURE-CONSOLIDATION-PLAN.md
**Date:** 2026-07-29
**Author:** ZCode (research + codebase synthesis)
**Scope:** Add a `zai` (GLM) provider adapter to mivia-agent after the architecture consolidation is complete.

> **⚠ BLOCKED.** This plan depends on **PROVIDER-ARCHITECTURE-CONSOLIDATION-PLAN.md** being implemented FIRST. That plan fixes the shared client (`CompatOptions`, `ErrorParser`, `ExtraHeaders` hooks) and installs the provider registry (`ProviderDescriptor` map). Once those foundations are in place, this ZAI adapter becomes a clean 1-file addition.
>
> Until then, attempting to add ZAI would require: editing 3+ files (hardcoded switches), working around the rigid `NewOpenAICompat` 5‑param API, and either forking `OpenAICompat` or adding fragile workarounds for ZAI's non‑standard error format.
>
> **This file is the durable research record and post‑consolidation implementation plan.** When the consolidation is done, execute this plan.

---

## 1. Summary & key finding

**ZAI/GLM exposes an OpenAI-compatible Chat Completions API.** The existing codebase centralizes all OpenAI-compatible logic in a shared client (`internal/provider/openai_compat.go`) used by the `deepseek` and `openrouter` adapters. Those two adapters are thin wrappers that only configure the shared client.

**ZAI is the same shape**, with two differences that require the consolidation hooks:

| Difference | What ZAI needs | Consolidation hook |
|------------|---------------|-------------------|
| Error format | `{"code": N, "message": "..."}` not `{"error": {...}}` | `CompatOptions.ErrorParser` |
| Required header | `Accept-Language: en-US,en` on every request | `CompatOptions.ExtraHeaders` |

Once the consolidation is done, ZAI becomes a **single-file adapter** + standard registration + TOML example.

### Confirmed ZAI API facts (from docs.z.ai)

| Item | Value |
|------|-------|
| Protocol | OpenAI Chat Completions compatible |
| Base URL (standard) | `https://api.z.ai/api/paas/v4` |
| Base URL (GLM Coding Plan) | `https://api.z.ai/api/coding/paas/v4` |
| Chat endpoint | `POST /paas/v4/chat/completions` (standard) or `POST /coding/paas/v4/chat/completions` (Coding Plan) |
| Auth | `Authorization: Bearer <ZAI_API_KEY>` (also supports JWT Token auth) |
| Required headers | `Content-Type: application/json`, `Authorization: Bearer`, `Accept-Language: en-US,en` (used in every ZAI curl example) |
| Request fields | `model`, `messages`, `temperature`, `max_tokens`, `stream`, `tools`, `tool_choice`, `thinking` — OpenAI shape + extensions |
| Streaming | SSE `data:` lines, terminates with `[DONE]` — OpenAI shape |
| Response fields (standard) | `choices[].message.{content, tool_calls}`, `finish_reason` — OpenAI shape |
| Response fields (ZAI-specific) | `choices[].message.reasoning_content` (thinking trace), `web_search[]` (search results), `usage.prompt_tokens_details.cached_tokens` |
| Error format | **⚠ NOT OpenAI-standard.** ZAI returns `{"code": <int>, "message": "<string>"}` at top level, NOT wrapped in `{"error": {...}}`. The shared `chatResponseBody.Error` struct expects OpenAI shape; without `ErrorParser` hook ZAI errors produce misleading `"empty choices"` messages. |
| Model names (non-exhaustive sample) | Flagship: `glm-5.2`, `glm-5.1`, `glm-5-turbo`, `glm-5`. Older: `glm-4.7`, `glm-4.7-flash`, `glm-4.7-flashx`, `glm-4.6`, `glm-4.5`, `glm-4.5-air`, `glm-4.5-x`, `glm-4.5-airx`, `glm-4.5-flash`, `glm-4-32b-0414-128k`. Vision: `glm-5v-turbo`. User-overridable via `[providers.zai].model` / `--model`. |
| API key env (convention) | `ZAI_API_KEY` |

Source: [docs.z.ai HTTP API guide](https://docs.z.ai/guides/develop/http/introduction), [ZAI Chat Completion reference](https://docs.z.ai/api-reference/llm/chat-completion), [ZAI OpenAI SDK guide](https://docs.z.ai/guides/develop/openai/python), [ZAI Error Codes](https://docs.z.ai/api-reference/api-code).

---

## 2. Architecture (post-consolidation)

After PROVIDER-ARCHITECTURE-CONSOLIDATION-PLAN.md is implemented, the architecture is:

- `internal/provider/provider.go` — `Providers map[string]ProviderDescriptor` (registry). `New()` does a map lookup.
- `internal/provider/openai_compat.go` — `CompatOptions` struct with `ExtraHeaders` + `ErrorParser` hooks.
- `internal/provider/deepseek.go` — registers into `Providers` with `CompatOptions{}` (no hooks).
- `internal/provider/openrouter.go` — registers into `Providers` with `CompatOptions{HTTPReferer, XTitle}`.
- `internal/provider/zai.go` — registers into `Providers` with `CompatOptions{ExtraHeaders, ErrorParser}`.
- `internal/config/load.go` — `resolveProvider()` looks up `provider.Providers[name]` for defaults.
- `internal/config/defaults.go` — only `DefaultProvider` constant; per-provider defaults in descriptors.

---

## 3. Implementation (post-consolidation)

### 3.1 Files to create

#### `internal/provider/zai.go` (~40 LOC)

Uses the post-consolidation `CompatOptions` and provider registry:

```go
package provider

func init() {
    Providers["zai"] = ProviderDescriptor{
        Name:             "zai",
        DefaultModel:     "glm-5.2",
        DefaultURL:       "https://api.z.ai/api/paas/v4",
        DefaultAPIKeyEnv: "ZAI_API_KEY",
        NewFactory:       NewZAI,
    }
}

// zaiErrorParser detects ZAI's flat error format: {"code": N, "message": "..."}
// and returns a formatted error. Returns nil for OpenAI-shaped errors so the
// default parser can handle them.
func zaiErrorParser(statusCode int, body []byte) error {
    var ze struct {
        Code    int    `json:"code"`
        Message string `json:"message"`
    }
    if err := json.Unmarshal(body, &ze); err != nil {
        return nil // not JSON, let default handler deal with it
    }
    if ze.Code == 0 && ze.Message == "" {
        return nil // empty or OpenAI-shaped, let default handler process it
    }
    return fmt.Errorf("zai: API error (code %d): %s", ze.Code, ze.Message)
}

// NewZAI returns a Z.AI (GLM) OpenAI-compatible completer.
func NewZAI(opts Options) (Completer, error) {
    base := opts.BaseURL
    if base == "" {
        base = Providers["zai"].DefaultURL
    }
    return NewOpenAICompat(CompatOptions{
        Name:    "zai",
        BaseURL: base,
        APIKey:  opts.APIKey,
        ExtraHeaders: map[string]string{
            "Accept-Language": "en-US,en",
        },
        ErrorParser: zaiErrorParser,
    }), nil
}
```

#### `internal/provider/zai_test.go` (~150 LOC, TDD)

Three-layer test strategy:

**Layer 1 — Constructor tests:**
- `NewZAI` with empty `BaseURL` produces client whose `baseURL` equals `Providers["zai"].DefaultURL`
- `NewZAI` with explicit `BaseURL` uses given URL
- `NewZAI` propagates `APIKey` correctly
- `NewZAI` sets `Accept-Language` header in `CompatOptions.ExtraHeaders`

**Layer 2 — Error intercept E2E:**
- `httptest.NewServer` returning ZAI-style error body `{"code": 1000, "message": "Invalid API Key"}` — assert adapter returns clear error, NOT `"empty choices in response"`
- `httptest.NewServer` returning OpenAI-shaped error `{"error": {"message": "bad request", "type": "invalid_request_error"}}` — assert default parser handles it
- `httptest.NewServer` returning valid response — assert `ChatTurn` works

**Layer 3 — Config wiring:**
- `resolveProvider` with `name: "zai"` sets `Model = "glm-5.2"`, `BaseURL = ZAI default`, `APIKeyEnv = "ZAI_API_KEY"`
- `provider.New()` dispatches to `NewZAI` correctly
- Full pipeline: test TOML → `config.Load` → `provider.New` → `ChatTurn` against httptest

### 3.2 Files to modify

#### `internal/config/defaults.go`

No changes needed — per-provider defaults moved to descriptors.

#### `internal/config/load.go`

No changes needed — `resolveProvider` already looks up `provider.Providers`.

#### `internal/provider/provider.go`

No changes needed — registry already handles dispatch.

#### `mivia.toml.example`

Add `[providers.zai]` block after `[providers.openrouter]`:

```toml
# Supported: deepseek (default), openrouter, zai
```
```toml
[providers.zai]
model = "glm-5.2"
api_key_env = "ZAI_API_KEY"
base_url = "https://api.z.ai/api/paas/v4"
# For GLM Coding Plan subscribers, use:
# base_url = "https://api.z.ai/api/coding/paas/v4"
```

### 3.3 Files NOT modified

- `internal/provider/openai_compat.go` — reused as-is (hooks already installed by consolidation)
- `internal/provider/openai_compat_stream.go` — reused as-is
- `internal/provider/provider.go` `Completer` interface — unchanged
- `internal/agent/loop.go` — unchanged
- `internal/chat/session.go` — unchanged

---

## 4. Post-consolidation touch count

| Action | Files changed |
|--------|---------------|
| ZAI adapter + tests | 2 created (zai.go, zai_test.go) |
| TOML example | 1 modified (10 lines) |
| **Total** | **3 files** |

Compare to pre-consolidation (6 files: zai.go, zai_test.go, defaults.go, load.go, provider.go, mivia.toml.example).

---

## 5. Verification plan

```text
go build ./...
go test ./internal/provider/... -race -count=1
go test ./internal/config/... -race -count=1
go vet ./...
make verify
make race
make secret-scan
```

All green. Optional smoke test (requires real key):
```text
ZAI_API_KEY=... ./mivia chat --provider zai --model glm-5.2 "say hello"
```

---

## 6. ZAI-specific features — first-class in v1

All ZAI-specific fields and behaviors are **integrated from day one**, not deferred. This requires adding hooks to the shared types during the consolidation phase (see PROVIDER-ARCHITECTURE-CONSOLIDATION-PLAN.md). Below is the complete specification.

### 6.1 `thinking` request parameter

ZAI supports `"thinking": {"type": "enabled"}` in the request body to enable reasoning/thinking mode.

**Consolidation hook needed:** Add `ExtraBody map[string]any` to `CompatOptions` in `provider/openai_compat.go`. When non-nil, `newRequest()` merges these key-value pairs into the JSON payload at the top level (alongside `model`, `messages`, etc.).

**ZAI adapter usage:**
```go
NewOpenAICompat(CompatOptions{
    Name:    "zai",
    BaseURL: base,
    APIKey:  opts.APIKey,
    ExtraHeaders: map[string]string{
        "Accept-Language": "en-US,en",
    },
    ExtraBody: map[string]any{
        "thinking": map[string]string{"type": "enabled"},
    },
    ErrorParser: zaiErrorParser,
})
```

The `thinking` field is always sent. If a user wants to disable thinking, they would override via a future `--no-thinking` flag or config option — deferred to post-v1.

### 6.2 `reasoning_content` in response

ZAI returns `choices[].message.reasoning_content` (a string) containing the model's reasoning/thinking trace alongside the visible `content`.

**Consolidation hook needed:** Add a `ReasoningContent string` field to the shared `Response` struct in `provider/provider.go`:
```go
type Response struct {
    Content          string
    ReasoningContent string   // ZAI-specific: thinking trace
    ToolCalls        []ToolCall
    FinishReason     string
}
```

**ZAI handling:** The shared `chatResponseBody.Message` struct already has anonymous fields — ZAI's `reasoning_content` is silently dropped because there's no Go struct field for it. During consolidation, add the field to the wire struct:
```go
type chatResponseBody struct {
    Choices []struct {
        Message struct {
            Content          string     `json:"content"`
            ReasoningContent string     `json:"reasoning_content"` // NEW — captures ZAI thinking trace
            ToolCalls        []ToolCall `json:"tool_calls"`
        } `json:"message"`
        // ... rest unchanged
    } `json:"choices"`
    // ...
}
```

The `ChatTurn` method copies `ReasoningContent` into the `Response`:
```go
return &Response{
    Content:          ch.Message.Content,
    ReasoningContent: ch.Message.ReasoningContent,
    ToolCalls:        ch.Message.ToolCalls,
    FinishReason:     ch.FinishReason,
}, nil
```

**Agent loop consumption:** The `Response.ReasoningContent` is surfaced to the user (e.g., printed to stderr before the assistant's content). The agent loop's `/status` slash command shows it. Implemented as a separate display channel in the CLI — the thinking trace is written to stderr while content goes to the normal output stream.

### 6.3 `web_search` in response

ZAI returns a `web_search` array with search result objects (title, content, link, icon, refer, publish_date) when the model performs web search.

**Consolidation hook needed:** Add `WebSearch []WebSearchResult` to the shared `Response` struct and the wire `chatResponseBody.Message` struct:
```go
type WebSearchResult struct {
    Title       string `json:"title"`
    Content     string `json:"content"`
    Link        string `json:"link"`
    Media       string `json:"media"`
    Icon        string `json:"icon"`
    Refer       string `json:"refer"`
    PublishDate string `json:"publish_date"`
}

type Response struct {
    Content          string
    ReasoningContent string
    ToolCalls        []ToolCall
    FinishReason     string
    WebSearch        []WebSearchResult  // ZAI-specific: web search results
}
```

**ZAI handling:** `ChatTurn` copies `WebSearch` into `Response`. The agent loop's output renderer displays search results as citations/footnotes.

### 6.4 Multiple base URLs — Coding Plan auto-detection

ZAI has two endpoints:
| Plan | Base URL |
|------|----------|
| Standard | `https://api.z.ai/api/paas/v4` |
| GLM Coding Plan | `https://api.z.ai/api/coding/paas/v4` |

**Problem:** Users who configure the wrong URL get HTTP 401/404 with no guidance.

**Solution (v1, not deferred):** The adapter implements **auto-detection on first request**:
1. Try the configured `base_url` first (user-set or default standard URL).
2. If the first non-stream request returns HTTP 401 or 404, AND the body contains a ZAI error code indicating plan mismatch (codes 1113, 1309, 1311, 1315 from the ZAI error table), AND the user hasn't explicitly set `base_url` in config, retry once with the alternative URL (`/coding/paas/v4` ↔ `/paas/v4`).
3. If the retry succeeds, store the correct URL in memory for the session.
4. If both URLs fail, return the combined error: `"zai: auth failed with both standard and Coding Plan URLs — check your API key and plan type"`.

**Implementation:**
```go
type zaiCompatWrapper struct {
    *OpenAICompat
    codingPlanFallback bool  // true → tried standard, now trying coding URL
}

func (z *zaiCompatWrapper) ChatTurn(ctx context.Context, req Request) (*Response, error) {
    resp, err := z.OpenAICompat.ChatTurn(ctx, req)
    if err != nil && z.isPlanEndpointMismatch(err) {
        // Flip to the other base URL and retry once
        z.baseURL = z.alternateURL()
        z.codingPlanFallback = true
        return z.OpenAICompat.ChatTurn(ctx, req)
    }
    return resp, err
}
```

**Error messages** are enhanced to suggest the alternative URL when HTTP 4xx is detected:
```go
func zaiErrorParser(statusCode int, body []byte) error {
    // ... detect ZAI error code ...
    if isPlanMismatch(code) {
        return fmt.Errorf("zai: %s (code %d). Tip: if you're on the GLM Coding Plan, use base_url = https://api.z.ai/api/coding/paas/v4", msg, code)
    }
    return fmt.Errorf("zai: API error (code %d): %s", code, msg)
}
```

### 6.5 JWT Token authentication

ZAI supports JWT Token auth (HS256) as an alternative to Bearer token. ZAI provides the `api_key` in the format `id.secret`; the JWT is generated client-side.

**Not implemented in v1.** The Bearer token path is sufficient (used in all ZAI curl examples). JWT would add a PyJWT-equivalent Go dependency. Marked as v2 enhancement.

---

## 7. Summary of consolidation hooks needed

These changes must be part of PROVIDER-ARCHITECTURE-CONSOLIDATION-PLAN.md for ZAI v1 to work:

| Hook | Where | Type | What it enables |
|------|-------|------|-----------------|
| `ExtraBody map[string]any` | `CompatOptions` in `openai_compat.go` | New field | ZAI `thinking` parameter |
| `ReasoningContent string` | `Response` in `provider.go` | New field | ZAI `reasoning_content` response |
| `WebSearch []WebSearchResult` | `Response` in `provider.go` | New type + field | ZAI `web_search` response |
| `WebSearchResult` struct | `provider.go` (new) | New type | ZAI web search result shape |
| `ChatTurn` copies new fields | `openai_compat.go` | Logic change | Propagate ZAI-specific response fields |
| Base URL auto-detection | `zai.go` (wrapper) | New logic | Dual endpoint handling |
| Error message enhancement | `zai.go` (error parser) | Logic change | Suggest alternative URL on 4xx |

---

## 7. Hostile challenge dispositions (from original review)

The original plan (Draft v2 → v3) was challenged and updated with 11 findings. All critical/high issues are resolved by the consolidation:

| # | Original finding | Resolution |
|---|-----------------|------------|
| 1 | **CRITICAL:** ZAI error format mismatch | ✅ `ErrorParser` hook handles it |
| 2 | **CRITICAL:** Accept-Language header missing | ✅ `ExtraHeaders` map includes it |
| 3 | HIGH: `reasoning_content` dropped | Acknowledged §6 |
| 4 | HIGH: Test strategy too thin | ✅ Three-layer tests in §3.1 |
| 5 | MEDIUM: Dual base URLs | Guidance in TOML example |
| 6 | MEDIUM: Model table incomplete | ✅ Full catalog in §1 |
| 7 | MEDIUM: `web_search` dropped | Acknowledged §6 |
| 8 | MEDIUM: ADLC violation | ✅ Acknowledged in consolidation plan |
| 9 | LOW: LOC padding | Minor |
| 10 | LOW: §8 missing | ✅ Now exists |
| 11 | LOW: CLI help cosmetic | Minor |
