# Native Anthropic Provider and Reasoning Effort Architecture Plan

**Status:** Ready for implementation — technically validated against current Anthropic API (2026-08) and current codebase; architecture-review PASS with non-blocking findings incorporated (see §8). One residual item (thinking-block `signature` field, §7 item 1) requires an empirical check against a live API at the start of Phase 2 before finalizing wire structs.
**Author:** mivia / MiviaLabs
**Date:** 2026-08-26 (revised)
**Topic:** Native Anthropic Messages API Provider (`anthropic`), Adaptive Thinking / Reasoning Effort Integration, Claude Model Auto-Resolution, and Proxy Incompatibility Remediation.

> **Revision note (2026-08-26):** The original draft (see `git log` for the prior version) mapped `reasoning.Level` to manual extended-thinking token budgets (`thinking: {type: "enabled", budget_tokens: N}`). That wire shape is **removed** (HTTP 400) on `claude-sonnet-5` — the model this plan defaults to — and on `claude-opus-5` / `claude-fable-5`; it survives only as a transitional escape hatch on `claude-opus-4-6` / `claude-sonnet-4-6`. This revision replaces §3.2/§3.4 with the current adaptive-thinking + `effort` model, adds required headers, refusal handling, and other corrections found during review. Codebase validation (see §6) confirms none of this is implemented yet, so the roadmap in §4 is unchanged in shape, only in wire-format detail.

---

## 1. Executive Summary

### 1.1 Context & Background
In current workflows, configuring Claude models (e.g. `claude-sonnet-5`) through local OpenAI-compatible proxies (such as `llmproxycli` or LiteLLM at `http://127.0.0.1:8317/v1`) frequently causes runtime failures with:
```
openai: provider error (HTTP 400, type invalid_request_error)
```
While OpenAI-compatible translation shims work well for basic text turns, they break when advanced agentic features are activated—specifically:
- **Chain-of-thought / Extended Thinking** (`reasoning_effort` vs Anthropic's `thinking` token budgets)
- **Multi-step Tool Turns with Thinking Replay** (OpenAI `reasoning_content` vs Anthropic `thinking` blocks with opaque cryptographic signatures)
- **Cache Breakpoints** (`cache_control` ephemeral blocks on system / tools / user messages)
- **Strict Parameter Constraints** (`max_tokens` strictly exceeding `budget_tokens`, temperature locks)

### 1.2 Objectives
1. **First-Class Native Anthropic Provider (`[providers.anthropic]`):** Implement a native `Completer` directly speaking the Anthropic Messages API (`/v1/messages`), supporting streaming SSE, tool calling, prompt caching, and thinking blocks.
2. **2026 Default Model Standards:** Standardize the default Claude flagship model to `claude-sonnet-5` across provider registries and agent definitions, with full support for `claude-opus-5` and `claude-haiku-5`.
3. **Reasoning Effort & Thinking Budget Mapping:** Define a clean mapping between Mivia's provider-neutral `reasoning.Level` (`low`, `medium`, `high`, `xhigh`, `max`) and Anthropic's token budget parameters.
4. **Under-the-Hood Auto-Routing & Compatibility:** Enable seamless routing when Claude models are invoked directly or via proxies, ensuring proper header injection (`anthropic-version`, `anthropic-beta`) and request shaping.

---

## 2. Root Cause Analysis: OpenAI Proxy vs Anthropic Wire Incompatibilities

| Dimension | OpenAI `/v1/chat/completions` (Proxy) | Anthropic `/v1/messages` (Native, `claude-sonnet-5`) | Failure Mode in Proxies |
| :--- | :--- | :--- | :--- |
| **Reasoning Control** | `reasoning_effort: "low"\|"medium"\|"high"` | `thinking: {"type": "adaptive"}` + `output_config: {"effort": "low"\|"medium"\|"high"\|"xhigh"\|"max"}` | HTTP 400: Unknown field `reasoning_effort`; `budget_tokens` variant also 400s on this model. |
| **Output Token Budget** | `max_tokens` is optional or standalone | `max_tokens` caps thinking + response text together; no `budget_tokens` to relate it to | Response truncates mid-answer (`stop_reason: "max_tokens"`) if `max_tokens` is sized for response-only, since Claude Sonnet 5 thinks by default. |
| **Reasoning Replay** | `reasoning_content: "text"` on assistant message | `{"type": "thinking", "thinking": "...", ...}` block, echoed back **unchanged** on the next turn | HTTP 400: Extra field `reasoning_content` not permitted, or a modified/dropped thinking block breaks ordering. **Open question:** whether adaptive-thinking blocks on Claude Sonnet 5 carry a `signature` field the same way pre-4.6 manual-thinking blocks did is not confirmed in current docs — verify against a live streamed response before hard-coding a `signature` field in the wire struct (see §7). |
| **System Prompt** | Message in `messages` with `role: "system"` | Top-level `system` string or array of text blocks | Message order rejection or loss of cache markers. |
| **Tool Calling** | `tools: [{type: "function", function: ...}]` | `tools: [{name, description, input_schema}]` | Schema translation mismatch or invalid tool call IDs. |
| **Prompt Caching** | Varies by proxy (some expect OpenAI, some Anthropic) | `cache_control: {"type": "ephemeral"}` (max 4 breakpoints; 1024-token minimum prefix on Sonnet-tier models) | Rejected array-content payloads on OpenAI endpoints. |
| **Sampling Params** | `temperature`/`top_p`/`top_k` generally accepted | Accepted only at their **default** value; any non-default value returns HTTP 400 on `claude-sonnet-5` | Proxies that pass through a non-zero `temperature` unconditionally 400 on this model. |
| **Refusals** | Not a distinct outcome | `stop_reason: "refusal"` with HTTP 200 (not an error) — Claude Sonnet 5 runs safety classifiers on cyber/bio-adjacent content | Code that reads `content[0]` unconditionally crashes on an empty or partial `content` array. |
| **Required Headers** | N/A | `anthropic-version: 2023-06-01` on every request; `anthropic-beta: <flag>` only for opt-in beta features (none required for adaptive thinking + effort — both are GA on this model) | Missing `anthropic-version` is itself a 400. |

---

## 3. Architecture & Technical Specification

```
                          ┌────────────────────────┐
                          │   mivia Agent Engine   │
                          │ (Agent Loop / Context) │
                          └───────────┬────────────┘
                                      │
                                      ▼
                      ┌────────────────────────────────┐
                      │    provider.NewForProvider     │
                      └───────┬────────────────┬───────┘
                              │                │
             Provider: "anthropic"             Provider: "llmproxycli" / "openrouter"
                              │                │
                              ▼                ▼
                ┌───────────────────┐    ┌───────────────────┐
                │ AnthropicCompleter│    │   OpenAICompat    │
                └─────────┬─────────┘    └─────────┬─────────┘
                          │                        │
                          ▼                        ▼
                POST /v1/messages        POST /v1/chat/completions
                (Native Anthropic)       (OpenAI wire format)
```

### 3.1 Provider Registry (`internal/providerregistry/registry.go`)
Register `"anthropic"` as a canonical provider:
```go
"anthropic": {
    Name:             "anthropic",
    DefaultModel:     "claude-sonnet-5",
    DefaultURL:       "https://api.anthropic.com/v1",
    DefaultAPIKeyEnv: "ANTHROPIC_API_KEY",
},
```

### 3.2 Reasoning Effort to Anthropic `thinking` + `effort` Mapping

**Revised approach (adaptive thinking, not manual token budgets).** `thinking: {"type": "enabled", "budget_tokens": N}` is removed on `claude-sonnet-5`, `claude-opus-5`, and `claude-fable-5` — it 400s. Anthropic's replacement is `thinking: {"type": "adaptive"}` (Claude decides how much to think per turn) combined with `output_config.effort`, a coarse `low`–`max` dial that scales both thinking depth *and* overall agentic behavior (tool-call consolidation, verbosity). This is a `Dialect` this plan should register as e.g. `DialectAnthropicAdaptive` in `internal/reasoning/reasoning.go` (see codebase validation in §6 — no Anthropic dialect exists today; the closest analog is `DialectThinkingEffort`, which should be reviewed for reuse before adding a new one).

Mivia's neutral `reasoning.Level` values (`Off, Minimal, Low, Medium, High, XHigh, Max` — these already exist in `internal/reasoning/reasoning.go:16-33` and need no change) map to wire values as follows:

| `reasoning.Level` | `thinking` | `output_config.effort` |
| --- | --- | --- |
| `Off` | `{"type": "disabled"}` — **only valid when the resolved `effort` is `high` or below**; combining `disabled` with `xhigh`/`max` returns 400 on `claude-opus-5` (unconfirmed whether `claude-sonnet-5` shares this exact constraint — verify, see §7) | n/a, or omit |
| `Minimal` | `{"type": "adaptive"}` | `"low"` |
| `Low` | `{"type": "adaptive"}` | `"low"` |
| `Medium` | `{"type": "adaptive"}` | `"medium"` |
| `High` | `{"type": "adaptive"}` | `"high"` (also the wire default if `output_config` is omitted entirely) |
| `XHigh` | `{"type": "adaptive"}` | `"xhigh"` |
| `Max` | `{"type": "adaptive"}` | `"max"` |

Omitting `thinking` entirely does **not** mean "no thinking" on `claude-sonnet-5` — it runs adaptive thinking by default (unlike Opus 4.7/4.8, where omission meant thinking-off). The provider implementation must therefore send an explicit `thinking` value on every request rather than relying on omission to express `reasoning.Off`.

**`max_tokens` sizing (replaces the old budget-based formula).** Since there is no `budget_tokens` to size against, `max_tokens` is a hard cap on **thinking + response text combined**. The provider must set `max_tokens` generously enough that adaptive thinking at the resolved effort level has headroom, or responses truncate mid-answer with `stop_reason: "max_tokens"`. Recommended floor, subject to tuning once integration tests run against the real API:

$$\text{wire\_max\_tokens} = \max(\text{req.MaxTokens}, \text{effort\_floor}(\text{level}))$$

where `effort_floor` is a per-level constant (e.g. `low`→8192, `medium`→16384, `high`→32768, `xhigh`/`max`→65536) chosen conservatively and validated empirically — Anthropic does not publish a formula relating `effort` to token consumption the way the old `budget_tokens` did, so this floor is a starting heuristic, not a documented invariant. Flag for Phase 4 verification.

**Thinking content visibility.** By default `thinking.display` is `"omitted"` — thinking blocks stream with an empty `thinking` text field (billed the same, just not surfaced). If mivia's UI wants to show reasoning progress, the provider must explicitly set `thinking: {"type": "adaptive", "display": "summarized"}`; otherwise a user watching a stream sees a long pause with no content before the answer starts.

### 3.3 Required Headers

Every request must carry:
* `x-api-key: <ANTHROPIC_API_KEY>` (or `Authorization: Bearer <token>` + `anthropic-beta: oauth-2025-04-20` if the provider ever supports OAuth-profile auth — out of scope for this plan's first cut)
* `anthropic-version: 2023-06-01` — **omitted entirely from the original draft; required on every request, not just beta ones.**
* `anthropic-beta: <flag>` — only when using a beta feature. Adaptive thinking and `output_config.effort` are both GA on `claude-sonnet-5` and need **no** beta header. Beta headers only come into play if a later phase adds task budgets (`task-budgets-2026-03-13`, GA-adjacent on Sonnet 5), compaction (`compact-2026-01-12`), or mid-conversation tool changes (`mid-conversation-tool-changes-2026-07-01`, Claude Opus 5 onward — **not** confirmed available on Sonnet 5, verify before using).

### 3.4 Refusal Handling

Claude Sonnet 5 runs safety classifiers on cyber- and bio-adjacent content. A declined request returns **HTTP 200** with `stop_reason: "refusal"` — not an error — and `content` may be empty (declined before any output, unbilled) or a partial array (declined mid-stream, partial output billed at normal rates). The `AnthropicCompleter` must check `stop_reason == "refusal"` before indexing into `content`, and surface this to the agent loop as a distinct outcome (not a transport error, not a normal completion) so callers can decide whether to retry, inform the user, or (later) opt into Anthropic's server-side `fallbacks` parameter. This has no analog in the OpenAI-compat wire format the proxy providers use today, so it is new surface area for `internal/provider.Completer` — worth flagging to reviewers as a interface-shape question (see §7).

### 3.5 Message & Tool Calling Wire Formats

#### System Prompt & Cache Markers
Anthropic expects system prompts at top level:
```json
{
  "system": [
    {
      "type": "text",
      "text": "You are mivia...",
      "cache_control": { "type": "ephemeral" }
    }
  ]
}
```

#### Multi-Turn Tool Exchanges with Thinking Replay
When preserving thinking from prior turns, the general shape is a `thinking` block followed by a `tool_use` block, echoed back **unchanged** (including a `thinking` block whose text is empty under the default `"omitted"` display setting):
```json
{
  "role": "assistant",
  "content": [
    {
      "type": "thinking",
      "thinking": "Need to inspect internal/provider/..."
    },
    {
      "type": "tool_use",
      "id": "toolu_01A",
      "name": "read_file",
      "input": { "path": "go.mod" }
    }
  ]
}
```
> **Open question (see §7):** the `signature` field shown in the original draft is documented for pre-4.6 manual (`budget_tokens`) thinking blocks, used to cryptographically verify the block wasn't tampered with on replay. Whether `claude-sonnet-5`'s adaptive-thinking blocks carry the same field, and whether the API rejects an adaptive block replayed without one, is **not confirmed** from current documentation and must be verified against a live streamed response before the Go struct for `ThinkingBlock` is finalized in Phase 2. Do not hard-code a required `signature` field until this is checked — get it wrong in either direction (omitting a required field, or requiring an absent one) and multi-turn tool replay breaks silently.
Tool responses:
```json
{
  "role": "user",
  "content": [
    {
      "type": "tool_result",
      "tool_use_id": "toolu_01A",
      "content": "module github.com/MiviaLabs/mivia-agent\n..."
    }
  ]
}
```

### 3.6 Streaming Protocol Support (`/v1/messages` SSE)
The stream processor handles Anthropic SSE event types:
1. `message_start`: Captures initial metadata and input token usage.
2. `content_block_start`: Distinguishes between `text`, `thinking`, and `tool_use`.
3. `content_block_delta`:
   - `thinking_delta`: Streams thought accumulation into reasoning builder. **Empty by default** unless the request set `thinking.display: "summarized"` (see §3.2) — a stream consumer expecting live reasoning text with no explicit `display` setting will see nothing here.
   - `signature_delta`: Retains block signature for subsequent roundtrip replay — **subject to the same open question as §3.3/§3.5**: confirm this event actually fires for adaptive-thinking blocks on `claude-sonnet-5` before building replay logic around it.
   - `text_delta`: Live writes assistant text to UI writer.
   - `input_json_delta`: Accumulates streaming tool arguments.
4. `content_block_stop`: Finalizes individual blocks.
5. `message_delta`: Extracts `stop_reason` (`end_turn`, `tool_use`, `max_tokens`, and — new to this plan — `refusal`, see §3.4) and output token usage.

---

## 4. Implementation Roadmap

### Phase 1: Core Registry & Reasoning Dialects
1. Add `anthropic` to `internal/providerregistry/registry.go` (`Descriptor{Name: "anthropic", DefaultModel: "claude-sonnet-5", DefaultURL: "https://api.anthropic.com/v1", DefaultAPIKeyEnv: "ANTHROPIC_API_KEY"}`).
2. **Update `internal/providerregistry/registry_test.go:87`** (`TestLookupRejectsUnknownEmptyAndOversized`) — this test currently pins `Lookup("anthropic")` to return `ok=false` as a negative-contract assertion. Adding the descriptor without touching this test will make it fail; it must be edited to remove `"anthropic"` from the rejected-names table, not left as-is or silently broken.
3. Add an adaptive-thinking dialect in `internal/reasoning/reasoning.go` (name TBD at review — `DialectAnthropicAdaptive` is descriptive, but check whether the existing `DialectThinkingEffort` variant already covers this shape closely enough to extend rather than duplicate; `internal/reasoning/reasoning.go:61-90` has the current `Dialect` enum and `:135-141` has the provider→dialect default map to extend with an `anthropic` entry).
4. Update `scripts/check_provider_docs.py` (validates `README.md` + `docs/architecture/overview.md` against the registry's provider list — currently 7 providers, no `anthropic` row) and any other registry-driven contract tests it doesn't already cover.

### Phase 2: Native Anthropic Client Implementation
1. Create `internal/provider/anthropic.go` implementing `Completer` (`internal/provider/provider.go:129`) — sends `anthropic-version: 2023-06-01` on every request (§3.3), maps `reasoning.Level` per the table in §3.2, and treats `stop_reason: "refusal"` (§3.4) as a distinct outcome rather than an error or a normal completion.
2. **Resolve the thinking-block `signature` open question (§3.3/§3.5/§3.6) before finalizing the wire structs** — a quick empirical check against a live streamed multi-turn tool-use response settles whether `ThinkingBlock` needs a `Signature` field and whether it's required on replay.
3. Create `internal/provider/anthropic_stream.go` for SSE block parsing per §3.6.
4. Wire `NewAnthropic` into the `builtins` factory list in `internal/provider/provider.go:283-291` (alongside the existing `NewDeepSeek`, `NewOpenRouter`, `NewZAI`, `NewOllama`, `NewLLMGateway`, `NewLLMProxyCLI`, `NewMiniMax`).

### Phase 3: Proxy & Model Dialect Auto-Detection
1. Update `internal/provider/llmproxycli.go` (currently a thin OpenAI-compat wrapper at lines 1-33 with a hardcoded `DialectOpenAI` reasoning dialect and `RequiresReasoningReplay: true` — it does **not** currently do any Claude-model-specific handling) to support model-specific dialect overrides when Claude models are routed through a local OpenAI-compat proxy.
2. Ensure `claude-sonnet-5` requests routed through `llmproxycli` degrade sensibly (proxies generally can't speak native `thinking`/`effort` — decide explicitly whether Phase 3 attempts translation or simply documents the limitation) rather than silently sending an incompatible payload.

### Phase 4: Verification & Docs
1. Unit tests covering message translation, tool invocation, thinking-block replay (once §3.3's open question is resolved), refusal handling, and streaming.
2. Empirically validate the `max_tokens` effort-floor heuristic from §3.2 against real traffic; adjust the constants.
3. Update `docs/product/config.md:183-197` (the "Provider support" list currently names exactly 7 providers with no `anthropic` entry or dedicated subsection, unlike `llmproxycli`/`ollama`/`llmgateway` which each get one) and `README.md`.

---

## 5. Summary of Benefits

- **Reliability:** Eliminates HTTP 400 errors caused by parameter mismatches in intermediate proxies.
- **Full Model Potential:** Unlocks full Claude Sonnet 5 capabilities including adaptive thinking, prompt caching, and fast multi-step tool execution.
- **Dual Flexibility:** Supports direct API access via `ANTHROPIC_API_KEY` and proxy routing via `llmproxycli`.

---

## 6. Codebase Validation (2026-08-26)

An `Explore` pass against the current `dev` branch confirmed the plan's premise: **none of Phases 1-4 exist yet**, and nothing else in flight duplicates this work.

- `internal/providerregistry/registry.go:17-46` — `descriptors` has exactly 7 entries (`deepseek`, `llmgateway`, `llmproxycli`, `minimax`, `ollama`, `openrouter`, `zai`). No `anthropic`.
- `internal/providerregistry/registry_test.go:87` — **pins `Lookup("anthropic")` to `ok=false`** as a negative-contract test. This must be edited, not just supplemented, in Phase 1 — see the roadmap note above.
- `internal/reasoning/reasoning.go:61-90` — `Dialect` enum has `DialectOpenAI`, `DialectOpenRouter`, `DialectOpenRouterOnOff`, `DialectThinking`, `DialectThinkingEffort`, `DialectThinkingPreserved`, `DialectNone`. No Anthropic-specific dialect. `Level` (`:16-33`) already matches this plan's `Off..Max` vocabulary and needs no change — only its Anthropic-side mapping (§3.2) is new work.
- `internal/provider/` has no `anthropic.go` / `anthropic_stream.go`; the `builtins` factory list (`provider.go:283-291`) has 7 entries, no `NewAnthropic`.
- `internal/provider/llmproxycli.go:1-33` is a thin OpenAI-compat wrapper with a hardcoded `DialectOpenAI` and no Claude-model-specific logic — confirms Phase 3's premise that no auto-detection exists today.
- `docs/product/config.md:183-197` lists the same 7 providers with no `anthropic` subsection.
- Commit `7c899c7a` ("docs(ai): add native anthropic provider and reasoning plan") is this plan document itself, docs-only. The four other recent commits (`fb83cbb4`, `6daa46ae`, `00d02f8d`, `ab1f0a9a`) are unrelated (empty-response retry/session-history fixes, first-run config bootstrap, onboarding plan).
- All `"anthropic"` string matches under `internal/` today are unrelated test fixtures using it as an arbitrary OpenRouter-routed model-name string (e.g. `internal/ui/screen/settings/mock_settings_test.go:682`), not a native provider.

---

## 7. Open Questions and Risks (post architecture-review)

1. **Thinking-block `signature` field on adaptive thinking — still open, verify empirically.** §3.2/§3.3/§3.6 flag that current Anthropic documentation confirms a `signature` field on pre-4.6 manual (`budget_tokens`) thinking blocks, but does not confirm the same for `claude-sonnet-5`'s adaptive-thinking blocks. This must be settled with a live API check before the `ThinkingBlock` Go struct is finalized in Phase 2 — getting it wrong breaks multi-turn tool replay silently (no compile error, a runtime 400 or corrupted replay).
2. **`max_tokens` effort-floor heuristic has no documented backing — still open.** Unlike the old `budget_tokens` invariant (`max_tokens > budget_tokens`, mechanically checkable), the replacement heuristic in §3.2 is an engineering guess calibrated by trial, not a published Anthropic contract. Ship it behind a config knob (per architecture-review AR-4) rather than hard-coded constants, so it can be retuned without a code change.
3. **`refusal` outcome — resolved by architecture-review (AR-1), no longer open.** `provider.Response.FinishReason` (`internal/provider/provider.go:119`) already exists as an opaque string threaded through `internal/agent/agentloop_completer.go` into `Loop.LastFinishReason` and consumed as free text (`internal/subagents/multi_step_schema.go:127-129`), never switch-cased against a closed set. Mapping Anthropic's `stop_reason: "refusal"` to `FinishReason: "refusal"` is a value-only extension of this existing field. **No new `Completer` interface method, sentinel error, or result-type field is needed** — Phase 2 should implement it this way and Phase 4 tests should assert `FinishReason == "refusal"` on a mocked refusal response.
4. **New `Dialect` constant is justified, but its wire-encoding home needs to be stated explicitly — refined by architecture-review (AR-2).** None of the 7 existing `Dialect` values can express Anthropic's shape (a third `thinking.type` value, `adaptive`, distinct from `DialectThinking`'s binary enabled/disabled; and `effort` nested under `output_config.effort` rather than a top-level field). A new Dialect constant is the right, minimal reuse of the existing `internal/reasoning` extension point — `internal/config` already validates dialects generically via `reasoning.ParseDialect` (`internal/config/model_spec.go:35`), so no config-schema change is needed beyond registering the new constant. **However:** the only current dialect→wire encoder, `reasoningBodyFields` (`internal/provider/reasoning.go:23-70`), is built for `OpenAICompat`'s flat-JSON-merge model and cannot represent Anthropic's native request structure (system/messages/thinking/output_config). Phase 2 must add a **separate** encoder (e.g. a private function in `internal/provider/anthropic.go`) that consumes `reasoning.Resolve()` the same way `OpenAICompat.reasoningFields` does, but is not a case inside `reasoningBodyFields`'s switch. The `CanGrade` doc comment in `internal/reasoning/reasoning.go` ("Keep it in step with provider.reasoningBodyFields") should be updated in the same change to name both encoder locations, or a future dialect addition will silently update only one.
5. **Mid-conversation system messages are not available on `claude-sonnet-5`** (only Opus 5/4.8/Fable 5/Mythos 5, per current docs). If mivia's agent loop ever wants to inject operator context mid-session without invalidating the prompt cache, this plan's default model doesn't support the mechanism other Claude tiers do — worth noting even though it's out of scope for this plan's first cut.
6. **Phase 3 scope is genuinely ambiguous.** "Ensure `claude-sonnet-5` is configured with appropriate thinking headers and token budgeting" (original wording) assumed a `budget_tokens`-based translation that no longer applies when routing Claude models through an OpenAI-compatible proxy like `llmproxycli`. Proxies generally cannot speak Anthropic's native `thinking`/`effort` fields at all — this plan should make an explicit decision (attempt best-effort translation vs. document the limitation and route Claude models to the new native `anthropic` provider by default) rather than leaving it implicit.
7. **Sampling-parameter enforcement differs from the original draft's "temperature locks" framing.** `claude-sonnet-5` accepts `temperature`/`top_p`/`top_k` at their default value; only a *non-default* value 400s. The wire builder needs to omit these fields rather than always omit-or-lock them, since a caller-supplied default value should pass through unchanged. This mirrors the existing `reasoningBodyFields` doc comment's finding for other providers (`internal/provider/reasoning.go:10-16`) that sampling-param rejection hypotheses should be verified against current docs, not assumed — good precedent to follow here too.

---

## 8. Architecture Review Result

```text
Result: PASS (with non-blocking findings — see AR-1..AR-4 above and NextAction)
Scope: docs/plans/anthropic-provider-and-reasoning-plan.md (this revision), compared against internal/provider, internal/reasoning, internal/providerregistry, internal/config, internal/agent on the current dev branch. No code exists yet for this feature (see §6).
Summary: The plan's boundary choices fit the existing Completer/provider-registry/reasoning-dialect abstractions with minor precision gaps, not structural ones; the refusal-handling "new interface" concern in the original open questions is resolved by an existing extensibility point, and the new-Dialect concern is justified but needs its second wire-encoder location named explicitly.
Evidence:
- internal/provider/provider.go:115-136 (Response.FinishReason, Completer interface) — confirms FinishReason is an open string, not a closed enum, so "refusal" is a value addition, not an interface change (AR-1).
- internal/agent/agentloop_completer.go:115,166, internal/agent/loop.go:70, internal/subagents/multi_step_schema.go:127-129 — traces FinishReason's only consumers; none switch on specific values, confirming AR-1's low blast radius.
- internal/reasoning/reasoning.go:60-141 (Dialect enum, CanGrade, defaultDialects) — no existing dialect matches Anthropic's adaptive+nested-effort shape (AR-2).
- internal/provider/reasoning.go:8-70 (reasoningBodyFields) — the sole current dialect encoder is OpenAICompat-specific flat-JSON-merge; a native Anthropic Completer cannot route through it (AR-2).
- internal/config/model_spec.go:35 — reasoning_dialect is validated generically via reasoning.ParseDialect; no config-schema change needed for a new Dialect constant.
- internal/provider/provider.go:283-291 (builtins factory list) — the plan's proposed NewAnthropic wiring point matches the existing pattern for all 7 current providers; no dependency-direction concern.
- internal/providerregistry/registry_test.go:87 — confirms the plan's own Phase 1 note (registry_test.go must be edited, not just supplemented) is accurate and already captured in the roadmap.
Findings:
- [AR-1] Refusal handling does not need a new Completer interface method, sentinel error, or new Response field — reuse Response.FinishReason with value "refusal". Consequence of not doing this: an unnecessary interface change ripples to every existing provider's mock/fake in tests for no behavioral gain. Alternative: extend FinishReason (adopted). Tradeoff: none identified — this is strictly simpler and already the pattern other finish states use. Action: update plan §3.4/§7 (done in this revision) and implement this way in Phase 2.
- [AR-2] The new Dialect's wire encoding must NOT be added as a case inside reasoningBodyFields (OpenAICompat-only); it needs a separate encoder in the native Anthropic provider file, with the CanGrade doc comment updated to reference both locations. Consequence of not doing this: a future engineer "completing" the Dialect switch in reasoningBodyFields would either dead-code a case that's never reached (native provider bypasses OpenAICompat) or, worse, wire the native effort concept into the wrong merge-map shape. Alternative: skip the shared Dialect enum entirely and keep Anthropic's mapping fully private to internal/provider/anthropic.go. Tradeoff: the shared-enum approach (adopted) keeps config validation and future /effort UI reporting free (per Resolve()'s doc comment on config/chat consumers); the fully-private alternative would need bespoke UI/validation wiring instead. Action: added to plan §7 item 4 and §4 Phase 2 note; Phase 2 implementation must not skip the CanGrade comment update.
- [AR-3] (non-blocking) The §3.2 max_tokens effort-floor heuristic is admittedly unverified; ship it as a tunable rather than hard-coded constants so Phase 4's empirical validation doesn't require a code change to adjust. Action: already reflected in plan §7 item 2's revised wording.
RejectedConcerns:
- Considered whether Phase 1's new Dialect constant belongs in a wholly separate package (e.g. an anthropic-specific reasoning package) instead of extending internal/reasoning.Dialect. Rejected: internal/reasoning's own package doc states it is deliberately the single provider-neutral vocabulary consumed by config, provider, and chat; a parallel enum would fragment that vocabulary for no demonstrated driver, and CanGrade/ParseDialect/defaultDialects all generalize cleanly to a new constant.
ResidualRisk: The thinking-block signature question (§7 item 1) and the exact anthropic-beta requirements for any Phase 3+ features remain unverified against a live API and cannot be resolved by static repository review — they need an empirical check during Phase 2 implementation.
NextAction: Implementation may proceed to Phase 1 (registry + dialect) once AR-1/AR-2's guidance is incorporated into the Phase 2 implementation approach (already reflected in this plan revision); resolve the §7 item 1 signature question empirically at the start of Phase 2 before finalizing wire structs.
```
