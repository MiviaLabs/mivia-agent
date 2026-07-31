# 38 — OpenAI provider (api.openai.com) with reasoning

**Status:** DESIGN — not yet implemented.
**Date:** 2026-08-02
**Depends on:** plan 37 (reasoning-effort field in the shared adapter).
**Blocks:** nothing. **Amends:** nothing.
**Blast radius:** LOW — one new OpenAI-compatible provider behind the existing factory
seam, same shape as `deepseek`/`openrouter`/`zai`. Static API key auth; no new auth
surface.

---

## 1. Goal

Add `openai` as a first-class built-in provider pointing at `api.openai.com/v1` with
an `OPENAI_API_KEY`, shipping GPT-5.x models with configurable reasoning effort. This
is the companion to plan 39 (xAI) — both are thin providers over the shared adapter,
both consume the reasoning-effort field plan 37 adds.

## 2. Why a registered provider, not just a config block

The factory and descriptor registries are **both closed** (see plan 34 §2):
`provider.NewForProvider` rejects any name not in `builtinFactories`, and `register()`
refuses a factory without a matching `Descriptor`. So even though OpenAI's API is
trivially OpenAI-compatible, mivia needs the descriptor + factory + registration
triplet that every other provider has.

## 3. Design

### 3a. Descriptor

`internal/providerregistry/registry.go`:

```go
"openai": {
	Name: "openai", DefaultModel: "gpt-5.6",
	DefaultURL: "https://api.openai.com/v1", DefaultAPIKeyEnv: "OPENAI_API_KEY",
},
```

### 3b. Factory + error parser

`internal/provider/openai.go` (new) — thin constructor calling
`NewOpenAICompatWithOptions`, with an `ErrorParser`/`NonRetryable` pair
(`openai_errors.go`) that classifies `invalid_api_key`, `insufficient_quota`,
`model_not_found` as permanent and maps them to clear messages naming
`OPENAI_API_KEY`. Mirrors the existing `zai_errors.go` shape. The parser returns `nil`
when the body doesn't match, falling through to the default `httpError` path.

### 3c. Register

`internal/provider/provider.go` — `registry.register("openai", NewOpenAI)` in
`registerBuiltins`.

### 3d. Reasoning

Consumed via plan 37. The OpenAI factory selects `openaiDialect{}` (plan 37 §3a),
which maps the internal `ReasoningLevel` to the top-level `reasoning_effort` string
on the Chat Completions body. No provider-specific reasoning code. The user sets
`reasoning = "high"` in `[chat]` (or `/reasoning high` per session) and the shared
adapter stamps the field and suppresses `temperature`.

## 4. Example config

Add to `mivia.toml.example`:

```toml
[providers.openai]
# Override CLI provider with --provider openai
# Get an API key at platform.openai.com
models = [
  { name = "gpt-5.6", context_window_tokens = 272000 },
  { name = "gpt-5.6-mini", context_window_tokens = 200000 },
]
default_model = "gpt-5.6"
api_key_env = "OPENAI_API_KEY"
base_url = "https://api.openai.com/v1"
```

Update `[provider] name` comment to list `openai`:
```toml
# Supported: deepseek (default), openai, openrouter, zai
```

And the reasoning key is documented once in `[chat]` (plan 37 §6), not per-provider.

## 5. Reasoning matrix for the example comment

GPT-5.6 supports: `none`, `low`, `medium`, `high`, `xhigh`, `max` (max is 5.6-only).
Default is `medium`. A one-line comment in the example block points at plan 37's
matrix rather than duplicating it.

## 6. What this does NOT do

- **No Azure OpenAI.** Different URL shape and `api-key` header (not `Bearer`).
  Separate provider (`openai-azure`), deferred.
- **No Responses API / `verbosity`.** Out of scope; `ExtraBody` can carry them later.
- **No new auth.** Static API key, identical to DeepSeek/z.ai.
- **No subscription import.** Plan 40 covers Codex/ChatGPT subscription auth.

## 7. Verification

- `go test ./internal/provider/...` — `openai_test.go`: builds with a key, sets
  `Authorization: Bearer`, fail-closes without a key, error parser maps known codes,
  non-retryable flags permanent errors
- `go test ./internal/providerregistry/...` — `openai` descriptor resolves
- `go build ./... && go vet ./...`
- Manual: `mivia --provider openai` with a real key completes a turn; with
  `reasoning_effort = "high"` set, the request includes the field and omits
  `temperature`

## 8. Invariant / rollback

No new invariant (additive behind a tested seam). If the error parser stops matching a
changed shape, it returns `nil` and the default path takes over — degraded messages,
no failure. Descriptor/factory cost nothing when unused.

## 9. Sequencing

1. `internal/providerregistry/registry.go` — add `openai` descriptor
2. `internal/provider/openai.go` + `openai_errors.go` (new) + tests
3. `internal/provider/provider.go` — register factory
4. `.mivia/mivia.toml.example` — `[providers.openai]` block + `[provider]` comment
5. Land **after** plan 37 so the reasoning field exists
