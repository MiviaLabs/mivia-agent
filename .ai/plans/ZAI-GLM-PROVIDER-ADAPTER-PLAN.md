# Plan: ZAI / GLM Provider Adapter

**Status:** Draft v2 (post challenge + validation — see §8 for dispositions)
**Date:** 2026-07-29
**Author:** ZCode (research + codebase synthesis)
**Scope:** Add a `zai` (GLM) provider adapter to mivia-agent so the agent can run against Z.AI's GLM models (e.g. `glm-5.2`, `glm-4.5`, `glm-4.5-air`) via the OpenAI-compatible chat completions API.

> **Note on the plan-as-file model.** The ADLC rule (`.ai/rules/05-adlc-agentic-development-lifecycle.md`) states a "zero files" storage model. This `.ai/plans/*.md` is a deliberate persistent-record exception, consistent with the established `.ai/plans/` directory (`ALLOWLIST-REFACTOR-PLAN.md`, `events-eventbus-refactor-plan.md`, etc.). The in-context Step 0 challenge still ran (3 challenger agents, dispositions in §8); this file is the durable record only.

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
| Chat endpoint | `POST /chat/completions` |
| Auth | `Authorization: Bearer <ZAI_API_KEY>` |
| Request fields | `model`, `messages`, `temperature`, `max_tokens`, `stream`, `tools`, `tool_choice` — OpenAI shape |
| Streaming | SSE `data:` lines, terminates with `[DONE]` — OpenAI shape |
| Model names | `glm-5.2`, `glm-4.5`, `glm-4.5-air` (user-overridable via `[providers.zai].model` / `--model`) |
| API key env (convention) | `ZAI_API_KEY` |

Source: [docs.z.ai HTTP API guide](https://docs.z.ai/guides/develop/http/introduction), [ZAI Chat Completion reference](https://docs.z.ai/api-reference/llm/chat-completion), [ZAI OpenAI SDK guide](https://docs.z.ai/guides/develop/openai/python).

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

#### `internal/provider/zai.go` (~20 LOC)

A thin adapter mirroring `deepseek.go` exactly. ZAI has no equivalent of OpenRouter's `HTTP-Referer`/`X-Title` ranking headers, so it passes empty strings (like DeepSeek).

```go
package provider

import "github.com/MiviaLabs/mivia-agent/internal/config"

// NewZAI returns a Z.AI (GLM) OpenAI-compatible completer.
func NewZAI(opts Options) (Completer, error) {
	base := opts.BaseURL
	if base == "" {
		base = config.ZAIDefaultURL
	}
	return NewOpenAICompat(config.ZAIName, base, opts.APIKey, "", ""), nil
}
```

#### `internal/provider/zai_test.go` (~90 LOC, TDD)

Mirrors the patterns in `openai_compat_test.go`: spin up a `httptest.NewServer` returning canned SSE, construct `NewOpenAICompat(config.ZAIName, srv.URL, "fake-key", "", "")` (or `NewZAI` with `BaseURL=srv.URL`), and assert:
- `ChatStream` emits content deltas to the writer.
- `ChatTurn` returns parsed `ToolCalls` from streamed tool-call deltas (the agent loop's real path).
- Default base URL (`NewZAI` with empty `BaseURL`) equals `config.ZAIDefaultURL` — verified by hitting the test server via the resolved base, OR by a dedicated test that the empty-base path does not panic and produces a client whose `baseURL` is the default (the existing tests already cover the default-URL behavior of DeepSeek/OpenRouter indirectly; we replicate the same shape).

These tests are intentionally thin because the *heavy* logic lives in the shared `OpenAICompat` client, which is already exhaustively tested. The ZAI test's job is to prove the wiring (right name, right default URL, delegation works).

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
```

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

1. **`Accept-Language` header.** ZAI's curl examples include `Accept-Language: en-US,en`. The shared `OpenAICompat` client does not send it. ZAI's API works without it (it's cosmetic locale hinting), so we do **not** add it. *Challenge item for validators:* confirm this doesn't matter for non-English content.
2. **Coding Plan base URL.** Documented as `…/api/coding/paas/v4`. We default to the standard URL and document the Coding Plan override in the TOML example only. *Challenge item:* is auto-detection needed? (Answer: no — user sets `base_url`; consistent with how DeepSeek/OpenRouter overrides work.)
3. **Model availability.** `glm-5.2` is the documented flagship as of July 2026; model lifecycle is ZAI's responsibility. Users override via `[providers.zai].model` or `--model`.
4. **No native GLM features (e.g. thinking/reasoning fields).** If ZAI exposes GLM-specific extensions (like a `thinking` block) beyond OpenAI shape, the shared client will ignore unknown response fields (it decodes into a fixed struct). That is acceptable for v1; native extensions would be a follow-up native adapter, explicitly out of scope here.

---

## 7. Out of scope

- A native (non-OpenAI) GLM adapter.
- Multimodal/image inputs (current `Message.Content` is a string; OpenAI multimodal needs array content — a separate cross-cutting change).
- GLM-specific response features (thinking, citations) beyond what `Response{Content, ToolCalls, FinishReason}` carries.
- Provider auto-discovery / registry refactor (the pending `REFACTOR-PLAN.md` is orthogonal).
