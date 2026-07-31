# 31 — Kimi (Moonshot AI) provider integration

**Status:** DESIGN-READY — compatibility decisions are locked and challenged.
Before writing code, re-run ADLC Step 0 against HEAD and disposition any new
architecture, security, and correctness findings.
**Date:** 2026-07-31
**Depends on:** the shipped provider-qualified model catalog (`29`).
**Blocks:** nothing.
**Blast radius:** MEDIUM — provider selection, request shaping, session history,
local persistence, retry classification, and configuration examples.

---

## 0. Challenge and validation record

On 2026-07-31, three independent reviews challenged this plan against the
current architecture, Kimi's official API documentation, and the project's
security/session rules. Their confirmed findings are incorporated in §§2–7:

- Kimi thinking behavior is per-model, not provider-wide; K2.6 requires an
  explicit preserved-thinking body.
- Kimi error handling must fail closed for malformed HTTP bodies and SSE error
  envelopes so the generic raw-body fallback cannot disclose provider text.
- Kimi tool shaping must deep-copy nested function maps before adding `strict`.
- Reasoning-only assistant messages need an explicit history/wire rule.
- The tracked 8192 completion cap is below Kimi's K2.x tool-use guidance.
- Persisting replayable reasoning requires a canonical security-policy
  disposition before it ships.

No reviewer found a reason to reject the OpenAI-compatible transport boundary,
the `kimi` provider name, or the explicit static model catalog. The next coding
turn must still perform the ADLC Step 0 revalidation against then-current HEAD.

## 1. Goal

Add Kimi Open Platform as the built-in provider `kimi`, with a safe,
OpenAI-compatible chat/tool/streaming integration that preserves the reasoning
state required by Kimi multi-turn and tool-call workflows.

The provider uses the public OpenAI-compatible endpoint
`https://api.moonshot.ai/v1` and `MOONSHOT_API_KEY` bearer authentication.
Sources: <https://platform.kimi.ai/docs/api/overview> and
<https://platform.kimi.ai/docs/api/tool-use>.

## 2. Locked compatibility decisions

| Concern | Decision |
|---|---|
| Provider identity | Use `kimi` in TOML and CLI selection. `moonshot` appears only in Kimi's API hostname and key name. |
| Default endpoint/key | `https://api.moonshot.ai/v1`; `MOONSHOT_API_KEY`. |
| Shipped catalog | `kimi-k3` (1,048,576 tokens); `kimi-k2.7-code`, `kimi-k2.7-code-highspeed`, and `kimi-k2.6` (262,144 tokens each). Default: `kimi-k3`. |
| Model discovery | Do not add remote discovery. mivia's TOML model list remains the explicit selectable allowlist; users can consult Kimi's authenticated `/v1/models` endpoint when curating it. |
| Temperature | Never send `[chat].temperature` to Kimi. Its current models have fixed temperature behavior and reject other values. |
| Completion cap | Encode mivia's completion cap as Kimi's `max_completion_tokens`, not the deprecated wire `max_tokens`. Raise both tracked examples' `[chat].max_tokens` from 8192 to 16384 when the Kimi catalog lands; K2.6/K2.7 tool workflows share that cap between reasoning and final text. |
| Tool strictness | Force `function.strict: false` for Kimi in v1. mivia continues validating every tool argument locally. This relaxes argument conformance only: every function still needs a valid name, `parameters`, and a root-object Kimi/MFJS-compatible schema. |
| Thinking state | Apply a selected-model policy: K3 and K2.7 replay reasoning; K2.6 replays it and sends `thinking:{type:"enabled",keep:"all"}`. Retain reasoning state locally only as the explicit security-policy exception in §5. |
| Retry/error privacy | Report only HTTP status and Kimi `error.type`; never echo a provider error message. Treat `exceeded_current_quota_error` as non-retryable; retain bounded retry for overload/rate-limit responses. |

Kimi requires complete assistant messages, including `reasoning_content`, to be
replayed for K3 and K2.7 multi-turn and tool workflows. K2.6 only preserves
historical reasoning when `thinking.keep` is `all`. Its current model parameter
constraints and context sizes are documented at
<https://platform.kimi.ai/docs/guide/use-thinking-models> and
<https://platform.kimi.ai/docs/api/models-overview>. Its error taxonomy is at
<https://platform.kimi.ai/docs/api/errors>.

## 3. Ground truth and gap

- `internal/providerregistry` owns provider defaults; `internal/provider` has
  one factory each for DeepSeek, OpenRouter, and z.ai.
- `OpenAICompat` already emits Chat Completions-compatible messages, bearer
  auth, SSE, tool calls, and `reasoning_content` responses.
- `provider.Response` has `ReasoningContent`, but `provider.Message` does not.
  The current agent loop renders/emits it and then drops it from history; plain
  streaming chat only receives a string. A factory-only Kimi addition would
  therefore fail Kimi's required multi-turn/tool-call replay contract.
- The shared client currently forwards configured `temperature` and emits the
  deprecated `max_tokens` wire field. The tracked examples set
  `temperature = 0`, which must not reach Kimi.
- Configuration validates all provider blocks, including inactive ones. Until
  the `kimi` descriptor and factory are implemented, a live `[providers.kimi]`
  block is a load error. Do not pre-add it to shipped TOML files.

## 4. Implementation design

### 4.1 Provider registration

Add a `kimi` descriptor in `internal/providerregistry/registry.go`:

```text
Name:             kimi
DefaultModel:     kimi-k3
DefaultURL:       https://api.moonshot.ai/v1
DefaultAPIKeyEnv: MOONSHOT_API_KEY
```

Register `NewKimi` in `internal/provider/provider.go`. `internal/provider/kimi.go`
uses `OpenAICompat`; it adds no independent HTTP client or SDK.

### 4.2 Shared request policy

Derive a Kimi policy from the selected `Options.Model` when `NewKimi` constructs
one provider generation. Model switching already creates a new generation, so
the policy is immutable for each client. Extend `CompatOptions`/`OpenAICompat`
with narrow, opt-in controls:

```go
IncludeReasoningContent bool
OmitTemperature         bool
UseMaxCompletionTokens  bool
ToolStrict              *bool
```

K3/K2.7 enable reasoning replay and omit a `thinking` field. K2.6 additionally
uses the existing copied `ExtraBody` option to set `thinking` to
`{"type":"enabled","keep":"all"}`. Every Kimi policy omits temperature, uses
`max_completion_tokens`, and sets `ToolStrict` false.

The request encoder must deeply copy the tools slice, each outer tool-spec map,
and each nested `function` map before inserting `strict: false`; it leaves the
parameter-schema map read-only. It must preserve ordinary providers' exact
payloads and reject an attempt to override reserved request fields as it does
today.

### 4.3 Reasoning-preserving history

Add `ReasoningContent string` to `provider.Message` and its API wire shape.
Only the selected Kimi policies that preserve thinking send it to a provider.
Include its bytes in `MessageTokens` so pruning remains conservative. Update the
history admission/drop rule: an assistant message with reasoning but no visible
content or tool calls is valid for a reasoning-preserving Kimi request; it stays
omitted for non-Kimi clients.

When an agent response contains reasoning, content, and/or tool calls, write its
reasoning onto the assistant history message. Change plain session streaming to call
`ChatTurn` with `Stream: true`, retain the response metadata, and append the
assistant message with reasoning after a successful turn. Existing session
JSONL persistence then round-trips the field without a new store format.

This is provider-required conversation state, not an operator log. It is still
network-provider data and may contain workspace-derived content. Before Wave 3
ships, update the canonical `docs/security/overview.md` with the explicit
retention decision: purpose (Kimi continuation), data owner (workspace user),
local storage/access paths, retention and session-deletion behavior, file
permissions, audit/diagnostic exposure, and the fact that configured redaction
cannot alter replayable reasoning. Tests use only synthetic reasoning strings;
provider error messages and credentials never enter errors, snapshots, or
fixtures. If that policy cannot be accepted, do not persist reasoning: Kimi
saved-session resume must fail closed/reset history rather than replay an
incomplete preserved-thinking conversation.

### 4.4 Error and retry behavior

Add `kimiErrorParser` and `kimiNonRetryable` in a Kimi-specific provider file.
They parse the documented `error.type` envelope with a bounded body, produce a
safe error such as `kimi: provider error (HTTP 429, type
exceeded_current_quota_error)`, and never include `error.message`. For every
non-2xx response, including malformed JSON or HTML, `kimiErrorParser` returns a
safe error so the shared client's raw-body fallback cannot run. On an HTTP-200
or SSE payload, it returns a safe error whenever an `error` envelope is present
and nil only for a normal completion/chunk. `kimiNonRetryable` marks quota
exhaustion permanent but leaves overload and short-term rate-limit envelopes to
the bounded shared retry policy.

## 5. Configuration and documentation delivery

Land this only after the descriptor/factory makes the configuration valid:

- Add the complete catalog in `[providers.kimi]` to `.mivia/mivia.toml` and
  `.mivia/mivia.toml.example`; leave their selected provider unchanged and set
  the shared `[chat].max_tokens = 16384` value in both files.
- Add `MOONSHOT_API_KEY=` to `.env.example`, without a value or key-like fixture.
- Update the owned canonical page `docs/product/config.md` with the Kimi setup,
  endpoint, key, supported catalog, context capacities, and the fact that
  `temperature` is intentionally ignored for Kimi.
- Update the owned canonical `docs/security/overview.md` with the §4.3
  reasoning-persistence policy before enabling K3/K2.7/K2.6 resume support.
- Update `docs/architecture/overview.md` only if its provider summary remains
  incomplete after the product-config update; do not duplicate setup guidance.

The catalog must stay static. Documentation may link Kimi's model-list endpoint
but must not promise automatic model discovery or account-entitlement probing.

## 6. TDD delivery waves

| Wave | Scope | RED/GREEN proof |
|---|---|---|
| 0 | ADLC challenge | Revalidate the already-resolved architecture (per-model policy), security (reasoning/error retention), and correctness (tool/history) findings against HEAD before edits. |
| 1 | Registry/config/factory | Kimi is selectable, resolves defaults/key/base URL, and fails closed without a key. |
| 2 | Request shaping | K3/K2.7/K2.6 policies produce their documented fields; K2.6 alone receives `thinking.keep=all`; Kimi omits temperature, uses `max_completion_tokens`, uses safe tool strictness, and leaves DeepSeek/OpenRouter/z.ai payloads unchanged. |
| 3 | Reasoning state | After the security-policy gate, streaming and non-streaming assistant/tool messages replay reasoning where supported; reasoning-only messages, token estimates, pruning, session save/load, deletion, and the chosen fail-closed resume behavior are proven. |
| 4 | Error/retry | Error messages exclude provider text; quota does not retry; overload/rate-limit paths remain bounded and cancellable. |
| 5 | Integration/docs | Local `httptest` config-to-provider-to-tool-loop test; TOML/env/docs updates; opt-in live smoke test. |
| 6 | Audit | Hostile bug audit reaches zero confirmed findings, then full verification. |

## 7. Required tests

- `internal/providerregistry`: lookup and sorted provider names include Kimi.
- `internal/config`: Kimi TOML resolves the documented catalog, defaults, base
  URL, and `MOONSHOT_API_KEY`; missing credentials disable selection.
- `internal/provider`: local HTTP tests assert the exact endpoint/auth payload,
  model-specific Kimi request transforms, K2.6-only thinking body, safe error
  parsing for HTTP/200-envelope/SSE/malformed-body paths, quota versus overload
  retry classification, SSE reasoning/tool-call assembly, complete-registry
  Kimi tool admissibility, and no mutation of caller-owned nested tool maps.
- `internal/agent` and `internal/chat`: a Kimi-shaped response survives a
  multi-step tool loop, plain streaming, reasoning-only response, pruning,
  save/load, session deletion, and a resumed session without losing required
  `reasoning_content`.
- `internal/cli`: cross-provider model selection still builds an isolated
  provider generation and preserves its existing failure behavior. Generate
  Kimi reasoning, switch to a non-Kimi provider and assert wire omission, then
  switch back and assert exact Kimi replay. Cover K2.6/K2.7 policy-class
  switches as well.

Tests must not call Kimi. Use `httptest`, fake key values such as
`kimi-test-key`, and synthetic reasoning only.

## 8. Verification and release gate

```text
go test ./internal/providerregistry ./internal/config ./internal/provider ./internal/agent ./internal/chat ./internal/cli -count=1
go test -race ./internal/provider ./internal/agent ./internal/chat ./internal/cli -count=1
go vet ./...
go build ./...
make docs-check
make secret-scan
make verify
```

After automated checks, run one opt-in live smoke test with a user-provided
`MOONSHOT_API_KEY`, a non-sensitive prompt, and a read-only tool call against
`kimi-k3`. It is never CI, never logs the key, and must not persist the prompt
or response as a fixture.

## 9. Rollback

Remove `kimi` from the selected provider/model catalog and retain existing
providers unchanged. Do not remove the generalized reasoning field if other
providers use it; if rollback must disable Kimi only, leave persisted session
data readable and omit `reasoning_content` from non-Kimi outbound requests.
