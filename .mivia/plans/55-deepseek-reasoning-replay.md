# 55 - Reasoning-content replay (generic provider capability)

**Status:** DESIGN - ADLC Step 0 CHALLENGED (2 hostile reviews) and LOCKED.
**Date:** 2026-08-03 (rev 2 after Step 0 disposition)
**Depends on:** `internal/provider` (Message/apiMessage/request path/context), `internal/agent`
(loop history), `internal/chat` (session persistence), `internal/contextmgr`
(planner fingerprint). Complements plan 37 (reasoning control) and plan 53
(reasoning-effort selection); unblocks the DeepSeek entries currently documented
as "unsupported" in `.mivia/mivia.toml`.
**Blast radius:** MEDIUM - touches the shared provider Message envelope, the
OpenAI-compat request marshalling + token estimation, the agent loop history,
and (for providers that adopt the capability) durable multi-turn state. The
capability defaults OFF so existing request bodies stay byte-identical.

## 1. Thesis

Some reasoning providers require the model's `reasoning_content` (chain of
thought) from a prior assistant turn to be **replayed verbatim** on subsequent
turns. DeepSeek v4's thinking mode is the documented case
(`api-docs.deepseek.com/guides/thinking_mode`): on tool-call turns the
`reasoning_content` must be passed back or the API returns 400. Other
providers/models may adopt the same contract in the future, so the mechanism
must be **generic, not DeepSeek-specific**.

Today `provider.Message` does not carry `reasoning_content`, so the field is
parsed from the response (`openai_compat.go`) and then **dropped on the floor**
(`loop.go:104-110` emits it as an event but never stores it in history).

Goal:
1. **Preserve** `reasoning_content` on the assistant `Message` in host history
   for every provider (generic; harmless when unused, future-proof).
2. **Replay** it on the wire only for providers that declare the capability —
   a per-provider (per-client) switch, default OFF so non-adopting providers
   (OpenAI, zai, OpenRouter) keep byte-identical request bodies and never
   receive an unexpected field.
3. **Count it** in every token estimator so the prompt budget, pruning, the
   contextmgr planner, and calibration see the replayed thinking (D1).
4. **Normalize legacy/empty tool-call turns** for adopting providers so a
   reasoning-less tool-call exchange can never ship a guaranteed-400 request
   (D2).
5. Adopt the capability for **DeepSeek** as the first concrete provider, then
   enable the deepseek reasoning dialect + config.

## 2. Verified baseline

- **Response parse:** `internal/provider/openai_compat.go:188-194` decodes
  `reasoning_content` into `Response.ReasoningContent` (non-stream and stream
  paths, `:261`, `openai_compat_stream.go:66,207`). Parsed for ALL compat
  providers, not just DeepSeek.
- **Consumption:** `internal/agent/loop.go:104-110` `emitReasoning` surfaces it
  as an `EventThinking` event and then it is gone. `Message` (provider.go:27-37)
  has no field for it; `apiMessage` (api_message.go:8-22) has no field either.
- **Wire building:** `toAPIMessages` (api_message.go:32-58) converts host
  `Message` → `apiMessage`; `marshalBody` (openai_compat_request.go:62-94)
  builds the request body. The replay decision belongs here, gated by a client
  capability. `toAPIMessages` has exactly one production caller
  (`marshalBody`) and one test caller (`context_test.go:449`).
- **Client capability seam:** all three factories build via
  `NewOpenAICompatWithOptions` (deepseek.go:25, zai.go:23-44,
  openrouter.go:27-38); `NewOpenAICompatWithOptionsAndRetry`
  (openai_compat.go:121-156) builds field-by-field and must carry it too.
  `CompatOptions` already has per-client flags (`CacheUsageEnabled`,
  `Reasoning` at openai_compat.go:69-71 — the field is `Reasoning`, not
  `DefaultReasoning`). A capability bool slots into this pattern. A
  dialect-level flag is wrong: `DialectThinkingEffort` is shared by GLM-5.2+
  and DeepSeek, and the replay requirement is orthogonal to the wire shape.
- **DeepSeek dialect:** `NewDeepSeek` (deepseek.go) sets **no default dialect**,
  deliberately (comment: "provider.Message does not preserve that field, so
  defaulting a dialect here would break multi-step tool turns"). `zai.go:41-44`
  sets its dialect via `defaultReasoningDialect("zai")` — the single-source
  pattern deepseek must follow, not a hardcoded spelling.
- **Persistence:** host `Message` is persisted in session JSONL
  (`chat/persistence_io.go:20-34`, `chat/persistence.go:213-255`). Adding
  `ReasoningContent string` with `json:"reasoning_content,omitempty"` persists
  automatically; old sessions decode with empty (inert). Note: session files
  DO gain the field for every provider whose response carried reasoning (it is
  parsed generically) — acceptable, no migration; only request bodies must stay
  byte-identical.
- **Token estimation (D1):** `internal/provider/context.go` `MessageTokens`
  (:39-49), `EstimateRequestCost` (:80-110), `EstimateMessageTokens`
  (:150-167) count Content/Name/ToolCallID/tool args only — **not**
  `ReasoningContent`. These feed `pruneHistory`, `promptBudgetErrorWithTools`,
  the contextmgr planner trigger/target, and the oneshot preflight. The
  contextmgr `plannerMessageFingerprint` (planner.go:308-327) is the
  compaction idempotency key and must distinguish reasoning-different content.
- **Contract (DeepSeek, verified from docs):** tool-call turns **must** replay
  `reasoning_content` or the API returns 400; non-tool turns "do not need to
  participate" and are "ignored" if passed; the docs' sample passes prior-turn
  tool reasoning into the next user turn. Thinking is on by default for
  DeepSeek v4.
- **Current state (D2 vector):** `.mivia/mivia.toml` runs deepseek-v4-pro as
  default with thinking on by default and NO reasoning configured; the loop
  DROPS `resp.ReasoningContent`. So every DeepSeek session persisted today
  contains assistant tool-call turns with **empty** reasoning. After adoption,
  resuming one (the loop always sends `Tools`) emits a reasoning-less
  tool-call turn on a tools-carrying request → guaranteed 400, permanently —
  the exact failure class `RepairToolPairing` (api_message.go:44-56) exists to
  heal.

## 3. Step 0 disposition (2 hostile reviews: architecture, correctness)

All findings dispositioned; incorporated below. **Confirmed → adopted:** D1
(token estimation: count ReasoningContent in all estimators + planner
fingerprint), D2 (adopting-provider normalization for reasoning-less tool-call
exchanges), capability name `RequiresReasoningReplay` (capability semantics,
matches the dialect-orthogonal design), wire BOTH compat constructors,
`deepseek.go` uses `defaultReasoningDialect("deepseek")` (single source of
truth), `CompatOptions.Reasoning` confirmed, name `context_test.go:449`,
openrouter-hosted-DeepSeek caveat, `ValidateToolPairing` hardening, prune +
`/effort off` edge tests. **Rejected:** dialect-level replay flag (shared by
GLM+DeepSeek; orthogonal to wire shape), config/model-level `reasoning_replay`
key in scope (deferred; documented caveat). **Verified non-defective:**
byte-stability of request bodies (omitempty + map round-trip + no pinned
bytes), wire role gate, whole-exchange pruning (never splits reasoning from its
pair; pruned exchange is absent so no replay required), non-tool emission
(harmless per docs), stream completeness (reasoning emitted before tool deltas;
complete in resp), subagent isolation (fresh history per subagent).

## 4. Locked design (generic)

### 4.1 `provider.Message` gains `ReasoningContent` (preserve, always)

- `provider.go` `Message` adds:
  ```go
  // ReasoningContent is the model's chain-of-thought for this turn, preserved
  // verbatim on the assistant message so providers whose thinking mode requires
  // replay (DeepSeek v4) can get it back on subsequent tool-call turns. Empty
  // for non-reasoning models and for non-assistant roles. Persisted in session
  // history; only ever re-emitted on the wire by providers that declare the
  // replay capability (CompatOptions.RequiresReasoningReplay). Counted by the
  // token estimators so prompt budgets see it.
  ReasoningContent string `json:"reasoning_content,omitempty"`
  ```
- Generic preserve step: the agent loop stores it for every provider; emission
  is the client's call.

### 4.2 `CompatOptions.RequiresReasoningReplay` (the capability switch)

- `openai_compat.go` `CompatOptions` gains:
  ```go
  // RequiresReasoningReplay reports whether this provider's wire dialect requires
  // the assistant reasoning_content to be echoed back verbatim on subsequent
  // tool-call turns (DeepSeek thinking mode). Default false: the field is never
  // emitted, so existing request bodies are byte-identical.
  RequiresReasoningReplay bool
  ```
- Store on `OpenAICompat` (`c.replayReasoning`). Wire into BOTH
  `NewOpenAICompatWithOptions` AND `NewOpenAICompatWithOptionsAndRetry`.

### 4.3 `apiMessage` + `toAPIMessages(replay bool)` (emit, gated + normalized)

- `api_message.go` `apiMessage` adds:
  ```go
  ReasoningContent string `json:"reasoning_content,omitempty"`
  ```
- `toAPIMessages(msgs []Message) []apiMessage` becomes
  `toAPIMessages(msgs []Message, replayReasoning bool) []apiMessage`
  (internal signature; update `marshalBody` + `context_test.go:449`). It copies
  `m.ReasoningContent` into `am.ReasoningContent` **only when ALL of**:
  - `replayReasoning` is true (provider opted in), AND
  - `m.Role == RoleAssistant`, AND
  - the value is non-empty.
  Otherwise the key is omitted — user/tool/system never carries it, and a
  non-adopting provider never sees it.
- **D2 normalization (adopting providers only):** when `replayReasoning` is
  true, an assistant message with `tool_calls` but **empty** `ReasoningContent`
  is an unrepairable pairing (the request would 400). Drop the whole exchange
  — the assistant tool-call turn AND its tool results — mirroring the existing
  unanswered-call repair in `RepairToolPairing`, so the request stays valid.
  (A reasoning-less tool-call turn arises from: pre-plan persisted DeepSeek
  sessions, a mid-session `/effort off` toggle producing a tool turn without
  thinking, or an interrupted turn.) If dropping the exchange would remove the
  only remaining user content, the request simply proceeds without that turn —
  never ship a guaranteed-400 body.
- **Defense-in-depth:** `ValidateToolPairing` rejects an assistant message with
  `ReasoningContent` on a non-assistant role (foreign/hand-edited JSONL).

### 4.4 Agent loop preserves it (generic)

- `internal/agent/loop.go` — wherever the assistant response is appended to
  history, copy `resp.ReasoningContent` onto the `provider.Message`:
  - `commitFinalAnswer` (loop.go:199-213, appends at :203-207; final assistant
    turn),
  - `processToolCalls` (loop_tools.go:58-85, appends at :63-68; the
    load-bearing one — assistant turn with tool calls).
  Both receive the SAME `resp` from `l.requestStep`; exactly one runs per step.
  - `recordInterruptedPartial` (loop.go:178-191) — no reasoning exists for an
    interrupted turn; a partial replay could itself 400. No change.
- `emitReasoning` stays (event surface) — it now also persists.
- The loop is provider-agnostic; emission is the client's call (4.3).

### 4.5 Multi-step re-request (no request-path change)

- The loop appends tool results after the assistant tool-call turn and
  re-issues the request with full history. Because `Message` carries
  `ReasoningContent` and (for an adopting provider) `toAPIMessages` re-emits it,
  the next request automatically contains the verbatim `reasoning_content` on
  the assistant message preceding the tool results. No request-path change
  beyond 4.2/4.3.

### 4.6 Token estimation counts it (D1)

- `internal/provider/context.go`: add `estimateTokens(m.ReasoningContent)` to
  `MessageTokens`, `EstimateMessageTokens`, and `EstimateRequestCost` (always —
  the field is preserved generically; conservative over-count for non-adopting
  providers is the safe direction). This fixes pruning, the hard prompt-budget
  gate, the contextmgr planner trigger/target, the oneshot preflight, and
  calibration.
- `internal/contextmgr/planner.go` `plannerMessageFingerprint`: include
  `ReasoningContent` so two plans over the same source range differing only in
  reasoning do not mint the same compaction idempotency key.

### 4.7 DeepSeek adopts the capability + dialect (first adopter)

- `internal/provider/deepseek.go` `NewDeepSeek`:
  ```go
  NewOpenAICompatWithOptions(CompatOptions{
      Name: "deepseek", BaseURL: base, APIKey: opts.APIKey,
      CacheUsageEnabled:       opts.CacheUsageEnabled,
      RequiresReasoningReplay: true,
      Reasoning:               defaultReasoningDialect("deepseek"),
  })
  ```
  (single source of truth — matches zai.go's pattern, not a hardcoded dialect).
- `internal/reasoning/reasoning.go` `defaultDialects`: add
  `"deepseek": DialectThinkingEffort` so config validation and `Resolve` agree.
- Then `.mivia/mivia.toml` can declare deepseek reasoning:
  ```toml
  { name = "deepseek-v4-pro", context_window_tokens = 1000000, max_output_tokens = 384000,
    reasoning_efforts = ["high", "max"], reasoning = "high", reasoning_dialect = "thinking_effort" },
  ```
  (v4 supports high/max; low/medium→high, xhigh→max. Same for
  deepseek-v4-flash.) `thinking_effort` verified correct for DeepSeek: it
  emits `{"thinking":{"type":"enabled"}}` + top-level `reasoning_effort`, both
  accepted together.

### 4.8 Future adopters + caveat (why this is generic)

- Any future provider/model that requires reasoning replay declares
  `RequiresReasoningReplay: true` in its factory (and its dialect default in
  `internal/reasoning`). No change to `Message`, `toAPIMessages`, the loop,
  estimators, or config schema. Non-adopting providers are untouched
  (byte-identical request bodies).
- **Caveat (documented):** a DeepSeek model hosted under the openrouter factory
  (`RequiresReasoningReplay=false`) will 400 on every tool-call turn with no
  config remedy today — that combination is unsupported for tool turns until a
  per-model `reasoning_replay` override exists (deliberately out of scope; a
  config-level override is the identified future step).

## 5. Invariants this plan must not break

- **Request-body byte discipline:** with `RequiresReasoningReplay=false` (all
  non-adopting providers, all non-reasoning turns) `toAPIMessages` emits
  nothing new and the marshalled request body is byte-identical to today.
  (Session files DO gain the preserved field for providers whose responses
  carried reasoning — acceptable, no migration.)
- **No field on wrong roles:** emission requires `RoleAssistant` AND non-empty
  AND capability on. `ValidateToolPairing` rejects non-assistant reasoning as
  defense-in-depth.
- **D2:** an adopting provider never emits an assistant tool-call turn with
  empty `ReasoningContent` — the exchange is dropped at emit, never a
  guaranteed-400 body.
- **Persistence compat:** old session JSONL without the field decodes to empty
  (inert). New sessions persist it; no migration. Pre-plan DeepSeek sessions
  are healed by the D2 normalization on their first post-adoption request.
- **Tool pairing / pruning:** `RepairToolPairing` and `pruneHistory` treat
  `ReasoningContent` as part of an assistant message, never split from it;
  pruning is whole-exchange (a pruned exchange is absent, so no replay is
  required and no 400 results).
- **Budget correctness (D1):** every estimator counts `ReasoningContent`, so
  pruning/planner/calibration see the replayed thinking; long CoT cannot
  silently exceed the window.
- **Streaming:** the stream path accumulates `reasoning_content` before tool
  deltas and into the shared `resp`; both stream and non-stream preserve it.
- **Fingerprint stability:** no `Task` fields touched; provider/agent message
  state + estimator only.
- **No cross-provider leak:** replay is to the SAME provider that produced it
  (capability is per-client). Switching providers mid-session does not emit the
  old provider's reasoning (the new client's capability gate decides).

## 6. Files

| File | Change |
|------|--------|
| `internal/provider/provider.go` (+`_test.go`) | `Message.ReasoningContent string` |
| `internal/provider/openai_compat.go` (+`_test.go`) | `CompatOptions.RequiresReasoningReplay`; store on client; wire BOTH constructors |
| `internal/provider/api_message.go` (+`_test.go`) | `apiMessage.ReasoningContent`; `toAPIMessages(msgs, replay bool)` gated emit + D2 drop-exchange normalization; `ValidateToolPairing` hardening |
| `internal/provider/openai_compat_request.go` | pass `c.replayReasoning` into `toAPIMessages` |
| `internal/provider/context.go` (+`_test.go`) | count `ReasoningContent` in `MessageTokens`/`EstimateMessageTokens`/`EstimateRequestCost` |
| `internal/contextmgr/planner.go` (+`_test.go`) | `plannerMessageFingerprint` includes `ReasoningContent` |
| `internal/provider/deepseek.go` | adopt capability + `defaultReasoningDialect("deepseek")` |
| `internal/reasoning/reasoning.go` (+`_test.go`) | `defaultDialects["deepseek"] = DialectThinkingEffort` |
| `internal/agent/loop.go` (+`_test.go`) | `commitFinalAnswer` + `processToolCalls` persist `resp.ReasoningContent` |
| `.mivia/mivia.toml` | deepseek models declare reasoning_efforts + default (high) |
| `.mivia/mivia.toml.example` | update the deepseek comment (now supported) |
| Integration test | DeepSeek-shaped wire replay (mock): assistant tool-call turn with reasoning_content → next request contains it verbatim; non-adopting provider → byte-identical request; legacy reasoning-less tool-call exchange → dropped, request valid |

## 7. Test strategy (TDD, named)

- `internal/provider` `provider_message_test.go`:
  - `TestMessageReasoningContentRoundTrips` (Marshal/Unmarshal preserves; empty → omitted),
  - `TestToAPIMessagesReplayGatedByCapability` (capability off → never emitted even when history has it; on → emitted for assistant non-empty),
  - `TestToAPIMessagesEmitsReasoningContentOnlyForAssistant` (user/tool/system → absent; assistant empty → absent),
  - `TestToAPIMessagesReasoningContentPreservedThroughToolTurn` (assistant tool-call turn with reasoning_content + capability on → apiMessage carries it),
  - `TestReasoningContentByteStabilityWhenReplayDisabled` (capability off → marshalled request byte-identical to pre-plan),
  - `TestAdoptingProviderDropsReasoningLessToolCallExchange` (D2: replay on, assistant tool-call turn with empty reasoning → exchange (turn + results) dropped, request valid, no reasoning_content sent),
  - `TestValidateToolPairingRejectsNonAssistantReasoning` (defense-in-depth).
- `internal/provider/context_test.go`:
  - `TestEstimatorsCountReasoningContent` (MessageTokens / EstimateMessageTokens / EstimateRequestCost include it; a long ReasoningContent counts toward PruneMessagesKeepTurns),
  - `TestPruneKeepsReasoningWithExchange` (whole-exchange prune retains/removes reasoning with its pair; pruned exchange absent).
- `internal/agent` `loop_reasoning_replay_test.go` (scripted completer):
  - `TestLoopPersistsReasoningContentOnToolCallTurn` (call 1 returns tool_calls + reasoning_content; the recorded assistant message carries it),
  - `TestLoopReplaysReasoningContentOnNextRequest` (call 2's request messages include the reasoning_content verbatim, for an adopting client),
  - `TestLoopPersistsReasoningContentOnFinalAnswer`,
  - `TestNonReasoningModelNoReasoningContent` (unset → history has none),
  - `TestEffortOffToolCallTurnDroppedForAdoptingProvider` (`/effort off` edge: tool-call turn with empty reasoning on an adopting client → exchange dropped at emit, request valid).
- `internal/reasoning` `reasoning_test.go`:
  - `TestDefaultDialectDeepSeekThinkingEffort`,
  - `TestDeepSeekConfigReasoningLoads` (mivia.toml deepseek entry validates: efforts high/max, default high, dialect thinking_effort).
- `internal/contextmgr` `planner_test.go`:
  - `TestPlannerFingerprintDistinguishesReasoning` (same source, different reasoning → different compaction key).
- Integration `internal/provider` `reasoning_replay_integration_test.go`:
  - mock HTTP server: adopting client → request 2's body contains the assistant message's `reasoning_content` verbatim; request 1 (no prior thinking) has none,
  - non-adopting client (default) → request bodies byte-identical across the same history,
  - legacy history (reasoning-less tool-call turn) on adopting client → exchange dropped, request 400-free.
- `internal/config` `shipped_example_test.go` / `TestShippedConfigsPassTheModelKeyAudit`: the updated deepseek entries must still load + validate (both shipped config and example).

## 8. Scorecard

| Criterion | PASS/FAIL |
|-----------|-----------|
| Compiles | PASS (additive fields; one internal signature change `toAPIMessages` + its two callers) |
| No cycles | PASS (provider, agent, reasoning, contextmgr already import each other in the existing direction) |
| No breaking API | PASS (`Message` gains an omitempty field; `toAPIMessages` is internal) |
| Generic (future providers) | PASS (capability switch on CompatOptions; no DeepSeek special-casing in Message/loop/toAPIMessages/estimators) |
| Budget-correct | PASS (D1: all estimators count ReasoningContent; fingerprint distinguishes it) |
| Legacy-safe | PASS (D2: adopting providers never emit a reasoning-less tool-call turn) |
| Testable in isolation | PASS (mock HTTP server + scripted completer) |
| Backward-compatible config | PASS (deepseek entries were unset; now opt-in via the shipped config — a deliberate, documented change) |
| Every function has a test | PASS (test table above) |

## 9. Rollback criterion

Plan is rejected if: (a) a non-adopting provider's request body changes for a
non-reasoning turn (byte-stability regression); (b) `reasoning_content` leaks
onto a non-assistant message and a provider rejects it; (c) the D2 normalization
cannot distinguish a reasoning-less tool-call turn from a legitimate one, or
drops a turn that must be kept; (d) the capability switch is not truly
per-provider (a non-adopting provider can receive another's reasoning); (e) the
estimator change breaks a pinned budget test. Rollback = revert the field +
capability + dialect + estimator lines; deepseek stays "unsupported" as today
(behavior-neutral).
