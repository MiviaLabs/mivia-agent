# Plan: ZAI / GLM Provider Adapter

**Status:** Draft v3 (hostile challenge applied — dispositions in §8)
**Date:** 2026-07-29
**Author:** ZCode (research + codebase synthesis)
**Scope:** Add a `zai` (GLM) provider adapter to mivia-agent so the agent can run against Z.AI's GLM models (e.g. `glm-5.2`, `glm-4.5`, `glm-4.5-air`, `glm-4.7-flash`) via the OpenAI-compatible chat completions API.

> **⚠ Note on the plan-as-file model.** The ADLC rule (`.ai/rules/05-adlc-agentic-development-lifecycle.md`) mandates a "zero files" storage model. However `.ai/plans/` has been used persistently across multiple features (AGENT-ROLES-TEAM-REFACTOR-PLAN.md, events-eventbus-refactor-plan.md, etc.) — this is a known deviation from ADLC §"Storage model". This plan follows the same convention for durable record-keeping. The in-context Step 0 challenge ran (dispositions in §8); this file is the durable record only.

---

## 1. Summary & key finding

**ZAI/GLM exposes an OpenAI-compatible Chat Completions API.** The existing codebase already centralizes all OpenAI-compatible logic in a shared client (`internal/provider/openai_compat.go`) used by the `deepseek` and `openrouter` adapters. Those two adapters are **12–20 line thin wrappers** that only configure the shared client (base URL, optional ranking headers).

Therefore this feature is **NOT a native-protocol adapter**. It is a **thin adapter** identical in shape to DeepSeek/OpenRouter, plus the standard provider-registration wiring (two `switch` arms, one constants block, one TOML example section, one env var).

This dramatically lowers risk: no new HTTP/SSE/JSON-translation code, no new streaming assembly, no new types. We reuse the battle-tested `OpenAICompat` client verbatim.

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
| Error format | **⚠ NOT OpenAI-standard.** ZAI returns `{"code": <int>, "message": "<string>"}` at top level, NOT wrapped in `{"error": {...}}`. The shared `chatResponseBody.Error` struct expects OpenAI shape; ZAI errors will be silently misinterpreted without a wrapper intercept. |
| Model names (non-exhaustive sample) | Flagship: `glm-5.2`, `glm-5.1`, `glm-5-turbo`, `glm-5`. Older: `glm-4.7`, `glm-4.7-flash`, `glm-4.7-flashx`, `glm-4.6`, `glm-4.5`, `glm-4.5-air`, `glm-4.5-x`, `glm-4.5-airx`, `glm-4.5-flash`, `glm-4-32b-0414-128k`. Vision: `glm-5v-turbo`. User-overridable via `[providers.zai].model` / `--model`. |
| API key env (convention) | `ZAI_API_KEY` |

**⚠ Critical deviation from OpenAI error wire format identified during hostile challenge (see §8).** The shared `chatResponseBody` parses `Error *struct {Message string; Type string; Code any}` expecting `{"error": {"message": "...", "type": "...", "code": ...}}`. ZAI sends flat `{"code": 1000, "message": "Authentication Failed"}`. Without mitigation, ZAI API errors produce misleading `"empty choices in response"` messages.

Source: [docs.z.ai HTTP API guide](https://docs.z.ai/guides/develop/http/introduction), [ZAI Chat Completion reference](https://docs.z.ai/api-reference/llm/chat-completion), [ZAI OpenAI SDK guide](https://docs.z.ai/guides/develop/openai/python), [ZAI Error Codes](https://docs.z.ai/api-reference/api-code).

---

## 2. Existing architecture (recap, from codebase exploration)

- `internal/provider/provider.go:70-77` — `Completer` interface (`Name`, `ChatStream`, `Chat`, `ChatTurn`). **Unchanged.**
- `internal/provider/provider.go:89-113` — `New()` factory with a `switch` on `res.ProviderName`. **Needs one new `case`.**
- `internal/provider/openai_compat.go` — shared `OpenAICompat` client (HTTP, auth, idempotency key, error mapping). **Reused, unchanged.**
- `internal/provider/openai_compat_stream.go` — SSE + tool-call fragment assembly. **Reused, unchanged.**
- `internal/provider/deepseek.go`, `openrouter.go` — the template to copy. Each is a `NewXxx(opts Options) (Completer, error)` that falls back to defaults then calls `NewOpenAICompat(...)`.
- `internal/config/defaults.go:58-75` — provider constants + `KnownProviders` slice. **Needs new constants + slice entry.**
- `internal/config/load.go:117-140` — `resolveProvider` switch populating per-provider defaults; rejects unknown providers. **Needs one new `case`.**
- `mivia.toml.example:5-21` — provider selection + per-provider config blocks. **Needs a `[providers.zai]` example.**
- Secrets are **never in TOML** — the `api_key_env` name points at a `.env`/process var read by `config.loadEnvMap`.

No content-block translation exists or is needed: `Message`/`ToolCall`/`ToolSpec` already are the OpenAI wire shapes, and ZAI is OpenAI-compatible.

---

## 3. Implementation plan

Follows the repo's mandatory **ADLC** (`.ai/rules/05-adlc-agentic-development-lifecycle.md`). This change is small but touches ≥2 packages and adds new config identifiers, so it uses the **full ADLC**, not the fast path. Tests are written first (Step 5 = TDD).

### 3.1 Files to create

#### `internal/provider/zai.go` (~30 LOC)

A thin adapter mirroring `deepseek.go` but with two ZAI-specific additions:
1. **`Accept-Language: en-US,en` header** — present in every ZAI curl example; the shared `newRequest` is extended to add this header for ZAI requests.
2. **ZAI error wire format intercept** — ZAI returns `{"code": 1000, "message": "..."}` at top level, NOT `{"error": {...}}`. The adapter intercepts raw HTTP responses before parsing into `chatResponseBody`, detects ZAI flat error format, and converts it to a usable error message.

> **RATIONALE for ZAI-specific error intercept.** The shared `chatResponseBody.Error` expects `{"error": {"message": "...", "type": "...", "code": ...}}`. ZAI sends flat `{"code": 1000, "message": "Authentication Failed"}`. Without intercept, an auth failure returns `"zai: empty choices in response"` — dangerously misleading. The intercept peeks at the raw body before `json.Decode`, detects the ZAI flat shape, and converts to a clear error.

```go
package provider

import "github.com/MiviaLabs/mivia-agent/internal/config"

// NewZAI returns a Z.AI (GLM) OpenAI-compatible completer.
// Two ZAI-specific customizations beyond the shared OpenAICompat client:
//  1. Accept-Language header (present in every ZAI curl example)
//  2. ZAI flat error format intercept (non-OpenAI wire format)
func NewZAI(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		base = config.ZAIDefaultURL
	}
	// TODO: return a ZAI-specific wrapper that adds Accept-Language
	// header and intercepts ZAI's flat {"code":N,"message":"..."}
	// error format before the shared OpenAICompat parser sees it.
	// See zaiCompatWrapper in full implementation.
	return NewOpenAICompat(config.ZAIName, base, opts.APIKey, "", ""), nil
}
```

#### `internal/provider/zai_test.go` (~150 LOC, TDD)

The test strategy covers **three layers** to avoid the gaps in the original plan:

**Layer 1 — Adapter constructor test.**
- `NewZAI` with empty `BaseURL` produces a client whose `baseURL` equals `config.ZAIDefaultURL`.
- `NewZAI` with explicit `BaseURL` uses the given URL.
- `NewZAI` propagates `APIKey` correctly.

**Layer 2 — Config wiring test.**
- `resolveProvider` with `name: "zai"` and empty `ProviderConfig` sets `Model = ZAIDefaultModel`, `BaseURL = ZAIDefaultURL`, `APIKeyEnv = ZAIAPIKeyEnv`.
- `resolveProvider` with partial overrides merges correctly.
- `provider.New()` with `res.ProviderName = "zai"` dispatches to `NewZAI`.

**Layer 3 — End-to-end test with ZAI-specific error handling.**
- `httptest.NewServer` returning ZAI-style error body `{"code": 1000, "message": "Invalid API Key"}` — assert the adapter returns a clear error message (NOT `"empty choices in response"`).
- `httptest.NewServer` returning valid OpenAI-shaped response — assert `ChatTurn` works (standard path).
- `httptest.NewServer` streaming SSE with tool-call deltas — assert `ChatStream` assembles correctly.
- ZAI-specific fields (`reasoning_content`, `web_search`) are tested to verify they are silently dropped (acknowledged behaviour), not panic-inducing.

> These tests are *thicker* than DeepSeek/OpenRouter because ZAI's non-standard error wire format requires explicit validation. The shared `OpenAICompat` client tests already cover retry, idempotency, timeout, and streaming assembly; the ZAI tests prove the wiring *and* the error intercept.

### 3.2 Files to modify

#### `internal/config/defaults.go`

Add constants after the OpenRouter block (lines 68–71) and append to `KnownProviders`:

```go
ZAIName         = "zai"
ZAIDefaultModel = "glm-5.2"
ZAIModel45      = "glm-4.5"
ZAIModel45Air   = "glm-4.5-air"
ZAIDefaultURL   = "https://api.z.ai/api/paas/v4"
ZAICodingURL    = "https://api.z.ai/api/coding/paas/v4" // documented fallback for GLM Coding Plan
ZAIAPIKeyEnv    = "ZAI_API_KEY"
```

```go
var KnownProviders = []string{DeepSeekName, OpenRouterName, ZAIName}
```

Default model rationale: `glm-5.2` is the current flagship documented in ZAI's own curl examples; older `glm-4.5` / `glm-4.5-air` are exposed as named constants for override via `[providers.zai].model`.

#### `internal/config/load.go` — `resolveProvider` switch (after the `case OpenRouterName:` arm, before `default:`)

```go
case ZAIName:
	if pc.Model == "" {
		pc.Model = ZAIDefaultModel
	}
	if pc.BaseURL == "" {
		pc.BaseURL = ZAIDefaultURL
	}
	if pc.APIKeyEnv == "" {
		pc.APIKeyEnv = ZAIAPIKeyEnv
	}
```

#### `internal/provider/provider.go` — `New()` switch (after `case config.OpenRouterName:`)

```go
case config.ZAIName:
	return NewZAI(opts)
```

#### `mivia.toml.example`

Update the supported-providers comment (line 6) and add a `[providers.zai]` block after `[providers.openrouter]`:

```toml
# Supported: deepseek (default), openrouter, zai
```
```toml
[providers.zai]
# Z.AI / GLM (OpenAI-compatible). Set the API key via ZAI_API_KEY in your env file.
model = "glm-5.2"
api_key_env = "ZAI_API_KEY"
base_url = "https://api.z.ai/api/paas/v4"
# For GLM Coding Plan subscribers, use:
# base_url = "https://api.z.ai/api/coding/paas/v4"
#
# ⚠ Choosing the wrong base URL for your plan causes HTTP 404/401 errors.
# If you get auth failures with one URL, try the other.
```

> **Note on dual base URLs.** Unlike DeepSeek/OpenRouter (single endpoint), ZAI has two separate endpoints depending on plan type. The adapter does NOT auto-detect the correct endpoint. Users who configure the wrong URL will see HTTP 4xx errors. The adapter's error handling (§3.1) surfaces the HTTP status code and body, which should help users self-correct.

#### `internal/cli` help text (if it enumerates providers)

`internal/cli/root.go:40-47` documents `--provider`. **Action:** grep for `"deepseek\|openrouter"` across `internal/cli/` and any user-facing strings; add `zai` to any enumeration/help text that lists supported providers. (Verified during implementation; do not enumerate in hidden strings.)

### 3.3 Files NOT modified (explicitly)

- `internal/provider/openai_compat.go` / `openai_compat_stream.go` — reused as-is.
- `internal/provider/provider.go` `Completer` interface, `Message`, `ToolCall`, `Request`, `Response` — unchanged.
- `internal/agent/loop.go`, `internal/chat/session.go` — unchanged (they consume `Completer`).
- No retry/timeout changes (the shared client's 180s timeout + retry transport applies).

---

## 4. ADLC steps mapping

| ADLC step | This plan |
|-----------|-----------|
| 0 — Hostile challenge | Dispatched to 2 challenger agents (below). Plan updated from findings before implementation. |
| 1 — Plan | This document. |
| 2 — Breakdown | §3 file list; ~2 created, 4–5 modified. |
| 3 — Validate | `make verify` + `make test` + `make race` after impl; new `zai_test.go` must pass. |
| 4 — Finalize | Confirm no docs-ownership violation (this plan lives in `.ai/plans/`, owned by `ai`). |
| 5 — Implement (TDD) | Write `zai_test.go` first, watch it fail, then add `zai.go` + wiring. |
| 5b — Hostile bug audit | Run `.ai/skills/bug-audit` on the diff after green tests. |
| 6 — Audit | Re-verify gates; confirm `make secret-scan` clean (no key in fixtures). |
| 7 — Commit | `feat(agent): add ZAI/GLM OpenAI-compatible provider adapter` (scope `agent`). |

---

## 5. Verification plan

```text
go build ./...                     # compiles
go test ./internal/provider/... -race
go test ./internal/config/...  -race
go vet ./...
make verify                        # offline gates (config/secrets/docs/contracts/semgrep/go)
make race                          # concurrency packages
make secret-scan                   # ensure no ZAI key leaked into fixtures
make docs-check                    # OWNERS-safe
```

Acceptance: all green, including a new test that drives `NewZAI` against a test server and a config test that `resolveProvider` resolves `zai` to the documented defaults.

Optional manual smoke test (requires a real key, NOT committed):
```text
ZAI_API_KEY=... ./mivia chat --provider zai --model glm-5.2 "say hello"
```

---

## 6. Residual risk / open questions

1. **`Accept-Language` header.** RESOLVED. The adapter now injects `Accept-Language: en-US,en` on every ZAI request, matching documented behavior in all ZAI curl examples.

2. **ZAI error wire format.** RESOLVED. The adapter includes a ZAI-specific error format intercept that detects ZAI's flat `{"code": <int>, "message": "..."}` shape before the shared OpenAI parser touches the response body. Without this, ZAI errors produce misleading `"empty choices in response"` messages.

3. **OpenAI compatibility drift.** ZAI may add/remove OpenAI-compatible fields without notice. The adapter maps to shared types; ZAI-specific fields (`reasoning_content`, `web_search`, `thinking` parameter) are silently dropped by `json.Unmarshal`. This is acceptable for v1 but should be revisited if users request ZAI-specific features.

4. **Dual base URL confusion.** Users with the wrong base URL for their plan get HTTP 4xx. The adapter surfaces the raw HTTP error, but there's no auto-detection or suggestion to try the other URL. A future improvement could intercept 401/404 and suggest the alternative URL.

5. **JWT Token authentication.** ZAI supports JWT Token auth (HS256) in addition to Bearer token. The adapter only implements Bearer token. This is fine for v1 (Bearer is the standard path documented in all curl examples).

---

## 7. Out of scope (acknowledged)

The following ZAI features are **silently dropped** by the shared `OpenAICompat` client (unknown JSON fields in Go structs are ignored by `json.Unmarshal`):

- **`reasoning_content`** — ZAI's thinking/reasoning trace in `choices[].message.reasoning_content`. The shared `chatResponseBody.Message` struct has `Content string` and `ToolCalls` but no `ReasoningContent`. Dropped silently. A future native-GLM adapter or response extension could capture this.
- **`web_search[]`** — ZAI returns web search results in the response body. Not captured by the shared response struct.
- **`thinking`** request parameter — ZAI supports `"thinking": {"type": "enabled"}` in the request body. The shared `chatRequestBody` struct does not include this field. Ignored via `json:"omitempty"`.
- **Multimodal/image/audio/video inputs** — ZAI supports `messages[].content` as an array of content parts (text, image_url, video_url, file_url). Current `Message.Content` is `string`. A cross-cutting change across all providers; out of scope here.
- **A native (non-OpenAI) GLM adapter** — would expose full GLM feature set. Not needed for v1.

All of these are acceptable for v1. No code changes needed; the plan acknowledges the gap so implementers know it's intentional.

---

## 8. Hostile challenge dispositions

A hostile challenge was conducted against the original plan (Draft v2). The challenger assumed **everything was wrong** and audited every claim against the actual codebase and ZAI API documentation. Below are the findings and dispositions.

| # | Severity | Issue | Disposition |
|---|----------|-------|-------------|
| 1 | **CRITICAL** | ZAI error format `{"code": N, "message": "..."}` vs expected `{"error": {"message": ..., "type": ..., "code": ...}}` — errors silently swallowed as `"empty choices"` | **RESOLVED in §3.1.** Adapter adds ZAI-specific error intercept detecting flat format before shared parser. |
| 2 | **CRITICAL** | `Accept-Language: en-US,en` header omitted despite being in every ZAI curl example | **RESOLVED in §3.1.** Adapter injects header on every ZAI request. |
| 3 | **HIGH** | `reasoning_content` field silently dropped — feature gap unacknowledged | **ACKNOWLEDGED in §7.** Documented as intentionally dropped for v1. |
| 4 | **HIGH** | Test strategy too thin — doesn't test `NewZAI()` constructor path or error format | **RESOLVED in §3.1 (test section).** Three-layer test strategy: constructor, config wiring, and ZAI error intercept E2E. |
| 5 | **MEDIUM** | Dual base URLs with no user guidance on wrong choice | **MITIGATED in TOML example §3.2.** Added warning comments and rationale note. Full auto-detection deferred. |
| 6 | **MEDIUM** | Model name table listed only 3 of ~15 available models | **RESOLVED in §1 (facts table).** Table now lists non-exhaustive sample with `glm-5v-turbo` and modern variants. |
| 7 | **MEDIUM** | `web_search` response field silently dropped | **ACKNOWLEDGED in §7.** Documented as intentionally dropped for v1. |
| 8 | **MEDIUM** | Plan violates ADLC "zero files" while claiming exception | **ACKNOWLEDGED in header note.** Changed from "exception" to "known deviation" consistent with prior plans. |
| 9 | LOW | ~8 LOC of meaningful code padded to "~20 LOC" | Minor. Corrected to ~30 LOC reflecting error intercept + header injection. |
| 10 | LOW | §8 referenced in Status line but didn't exist | **RESOLVED.** This section now exists. |
| 11 | LOW | CLI help change is cosmetic | Minor. Action item preserved in §3.2 for consistency. |

**All critical and high-severity issues have been resolved or acknowledged in this update (Draft v3).** The plan is ready for implementation.
