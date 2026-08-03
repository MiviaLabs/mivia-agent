# 56 - Input-token cache misses: measurement, attribution, remediation

**Status:** DESIGN - ADLC Step 0 CHALLENGED (hostile audit) and LOCKED.
**Date:** 2026-08-03 (rev 2 after Step 0 disposition)
**Depends on:** plan 55 (reasoning-content replay — one of the levers), the existing
`CacheUsage` capture + `EventCacheUsage` surface, the contextmgr planner, tool-result
budgets. The remediation levers are studied here and adopted as follow-up plans.
**Blast radius:** LOW-MEDIUM - adds observability (per-turn + session cache
accounting + attribution). No change to the request path unless a lever is adopted.

## 1. Thesis

Input-token cost in long agent sessions is dominated by **prompt-cache misses**:
implicit prefix caching means only the *stable, repeated prefix* is billed as a
hit; every byte that changes, is inserted, or is removed invalidates the cache
from that point. The user reports **higher input-token misses than usual** on a
z.ai GLM-5-Turbo coding-plan session. This plan makes misses **measurable and
attributable** (per turn + per session + per driver), then studies the levers.
It deliberately does NOT redesign pruning before measurement.

## 2. Verified baseline (what already exists + what the audit proved)

- **Cache accounting is captured:** `internal/provider/cache.go` decodes
  `CacheUsage{Reported, Style, InputTokens, CachedInputTokens, CacheWriteTokens}`
  from both wire conventions (DeepSeek flat, OpenAI/OpenRouter/zai nested
  `prompt_tokens_details.cached_tokens`); `deriveCacheUsage` clamps negatives;
  `Reported=false` means "not reported".
- **It is emitted per STEP, not per turn:** `internal/agent/loop.go:330` calls
  `EmitCacheUsage` inside `requestStep` — once per provider call; one user turn
  runs multiple steps. The plan must aggregate per-step usage into a per-turn
  value (D3).
- **Prune has TWO mechanisms:** the legacy `pruneHistory` (loop.go:129-140,
  emits `EventPrune`, gated by `opts.PreparationManager == nil`) and the
  **production default** contextmgr planner path (`context.go:20-23` skips
  `pruneHistory` when `PreparationManager != nil`; the planner compacts via
  `planner_elision.go` `planCompact` = drops old units + elides bodies, and
  emits NO `EventPrune`). The loop accumulates `turnBeforeTokens/turnAfterTokens`
  and `turnElidedBytes` (context.go:62-77) per turn — **this is the production
  compaction signal** (D1).
- **load_tools is off-by-one:** `chat/admission.go` stages admissions and
  publishes the new surface AFTER the turn commits; the loop captures
  `toolSpecs` once per Run. So an admission invalidates turn N+1's first
  request, not turn N — attribution must lag one turn (D2).
- **Tool-result bytes:** shaped bodies are appended at `loop_tools.go:40-49`;
  no per-turn accumulator exists today (add one).
- **Subagents:** get `OnEvent` only, no `EventBus` and no planned sink — their
  cache usage reaches neither `/usage` nor the bus stream (D5).
- **z.ai reports cache** as nested `prompt_tokens_details.cached_tokens`
  (decoded by `deriveCacheUsage`); coding-plan pricing has a cached-input
  multiplier. Missing replay (plan 55) means reasoning is re-derived each turn
  and never becomes a cacheable prefix.

## 3. Hypotheses (drivers of miss%, ranked)

- **H1 — Prune/compact invalidates the prefix (highest).** Dropping an old
  exchange from the head, or eliding a middle body, changes bytes before the
  current position → prefix cache invalidated from that point.
- **H2 — Elision rewrites, not drops.** Summarizing an old tool result replaces
  bytes → same invalidation, subtler. In the planner path BOTH happen in one
  compaction, so H1/H2 are conflated unless measured separately.
- **H3 — New uncached content every turn (unavoidable, bounded).** Tool results
  append new bytes; bounded by the tool-result budgets. Legitimate miss.
- **H4 — Mid-session `load_tools`/system-prompt change.** Publication replaces
  the system prompt + tools array at a turn boundary → the NEXT turn's first
  request is a head-of-prompt change (same mechanism as H1).
- **H5 — Missing reasoning replay (plan 55).** On DeepSeek/z.ai, thinking is
  re-derived each turn and never becomes a cacheable prefix.
- **H6 — Session/restart + idle expiry.** Prefix caches expire on short TTLs;
  an idle gap of minutes → next first request is a full miss with no
  prune/elide/load driver. Intra-session expiry is a real unmeasured driver.

## 4. Phase A — Observability (core deliverable)

### 4.1 Per-turn + session cache accounting

- `internal/chat` session state gains cumulative counters
  `{InputTokens, CachedInputTokens, CacheWriteTokens, MissTokens, MissPct}` and
  a bounded per-turn slice (last N).
- **Aggregate per STEP into per-TURN:** the loop accumulates each step's
  `CacheUsage` (sum) across a `Run` and feeds the sink **once at turn end**
  (D3). `Options.CacheSink func(provider.CacheUsage)` — nil = off. Subagent
  loops get their own fresh sink / fresh counter namespace (D5), or `/usage` is
  explicitly labeled "root-loop only"; the bus stream gets subagent usage via a
  per-subagent sink (origin-stamped) so spend accounting is complete.
- Persistence: counters survive save/load/resume (`TestCacheCountersSurviveSaveLoad`);
  `/clear` resets them (D7).

### 4.2 Surface

- `/usage` slash command + TUI status line (driven by the SAME counters, to
  avoid drift): per-turn (input/cached/write/miss/hit%), session totals + hit%,
  last compaction (tokens + mode), load-tool admissions, subagent usage.
- Machine-readable: reuse the existing `cache_usage` + `compaction`/`prune`
  event streams (do NOT invent a parallel event kind for the numbers).

## 5. Phase B — Attribution (the analysis instrument)

- Emit `cache_context` once per turn (only when the provider reported cache):
  `{turn, provider, model, input, cached, write, miss, mode
  (legacy-prune|planner), compacted_tokens_this_turn = turnBeforeTokens −
  turnAfterTokens, elided_bytes_this_turn, tool_result_bytes_this_turn,
  load_admissions_prev_turn (one-turn lag, D2), system_prompt_changed,
  last_turn_gap_seconds, session_age_turns}`.
- **Attribution sources (audit-corrected):**
  - compaction: `turnBeforeTokens/turnAfterTokens` (production planner path,
    D1) + `EventPrune` delta (legacy path) + `mode` field,
  - elision: `turnElidedBytes` (already per-turn),
  - tool bytes: new per-turn accumulator at the `loop_tools.go` append site,
  - load_tools: `admissionPublications` carried with a one-turn lag (D2),
  - gap: wall-clock since the previous turn (D6).
- Subagent turns: `cache_context` carries an `origin` (root|subagent:task) so
  the attribution is complete without polluting root prefix accounting (D5).

## 6. Phase C — Levers (studied here; adopted as follow-up plans)

- **L1 — reasoning replay (plan 55).** Adopt for deepseek + z.ai; measure
  cached-token growth across tool turns. Expect reasoning blocks to become hits
  after turn 1.
- **L2 — prune policy (the big one).** Requires a **dedicated experiment**, not
  the Phase A/B harness alone (audit Q6):
  - identical scripted workload fixture (deterministic mock provider; with a
    real provider, drop-vs-elide changes what the model sees → prompts diverge
    → invalid comparison),
  - ≥2 prune/elide policy configs (a policy knob the experiment introduces),
  - comparison metric: cumulative miss tokens + invalidation count.
- **L3 — tool-result budgets** (`batch_result_budget_bytes` /
  `max_tool_result_bytes`): shrink per-turn new bytes (H3). Already knobs.
- **L4 — deferred tool loading / head stability:** admit at session start or
  keep the schema block order stable; measure invalidation at load boundaries
  (H4).
- **L5 — session continuity + idle:** prefer resume; measure whether a resumed
  session's first request hits; understand idle-expiry (H6).

## 7. Files (Phase A + B; levers are follow-up plans)

| File | Change |
|------|--------|
| `internal/chat` (+`_test.go`) | session cache counters (cumulative + bounded per-turn slice), save/load + `/clear` reset |
| `internal/agent/loop.go` (+`_test.go`) | per-step → per-turn `CacheUsage` aggregation; `Options.CacheSink`; per-turn tool-result-byte accumulator; emit `cache_context` with mode/compacted/elided/load-lag/gap |
| `internal/events` (+`_test.go`) | typed `CacheContextEvent` (origin-stamped) |
| `internal/cli` (+`_test.go`) | `/usage` slash handler + TUI status line from the SAME counters |
| Integration test | scripted long session (mock provider with cache usage) → per-turn `cache_context` populated; drivers attributable (incl. one prune turn + one load_tools-lag turn + one idle-gap turn) |

## 8. Test strategy (TDD, named)

- `internal/chat` `cache_counters_test.go`:
  - `TestCacheCountersAccumulatePerTurn` (input/cached/miss/write accumulate; MissPct correct),
  - `TestCacheCountersBoundedTurnSlice` (last-N retention),
  - `TestCacheCountersIgnoreUnreported` (Reported=false contributes nothing),
  - `TestCacheCountersSurviveSaveLoad` (persist through resume),
  - `TestCacheCountersResetOnClear` (`/clear` resets),
  - `TestCacheCountersClampNegativeMiss` (cached > input → miss clamped at 0, D8).
- `internal/agent` `cache_context_test.go`:
  - `TestLoopEmitsCacheContextPerTurn` (mode, compacted = before−after, elided, tool-result bytes, load-lag, gap),
  - `TestCacheContextAggregatesMultiStepTurn` (5-step turn → ONE per-turn entry, D3),
  - `TestCacheContextOmitsUnreported`,
  - `TestCacheSinkFedOncePerTurn` (per-turn, not per-step),
  - `TestSubagentCacheContextOriginStamped` (subagent turns carry origin, D5),
  - `TestPruneModeLegacyVsPlanner` (D1: legacy path EventPrune delta; planner path before/after; both surface),
  - `TestLoadAdmissionsLagOneTurn` (D2: admission in turn N appears in turn N+1's context).
- `internal/events` `cache_context_test.go`: `TestCacheContextEventTyped` (round-trip).
- `internal/cli` `usage_slash_test.go`:
  - `TestUsageSlashShowsPerTurnAndSession`,
  - `TestUsageSlashNoReportedCache` (empty state),
  - `TestUsageSlashShowsSubagentUsage` (origin-stamped, D5).
- Integration `internal/chat` `cache_attribution_integration_test.go`:
  - scripted multi-tool session against a mock provider reporting cache usage:
    one prune turn (D1), one big tool-result turn (H3), one after a load_tools
    admission (H4, one-turn lag) → each `cache_context` attributes its miss to
    the right driver.

## 9. L2 experiment design (dedicated; not the A/B harness)

- Workload fixture: a fixed scripted multi-tool conversation (deterministic
  mock provider, cache-reporting) replayable under any policy.
- Policy knob: a test-only `PrunePolicy` config (drop-at-threshold vs
  elide-oldest vs fewer-bigger-drops) — introduced by the follow-up, not by
  Phase A/B.
- Metric: cumulative miss tokens + invalidation count per policy, compared
  across identical workloads.
- Gate: an L2 policy lever ships ONLY with this experiment's before/after
  numbers.

## 10. Scorecard

| Criterion | PASS/FAIL |
|-----------|-----------|
| Compiles | PASS (additive fields/events/handler; one new Options sink) |
| No cycles | PASS (chat/agent/events/cli existing direction) |
| No breaking API | PASS (additive only; `EmitCacheUsage` unchanged) |
| Measurable | PASS (per-turn + session counters; machine-readable events) |
| Attributable | PASS (audit-corrected: mode, before/after compaction, elided, tool bytes, load-lag, gap) |
| Subagent-complete | PASS (origin-stamped per-subagent sink; `/usage` shows both) |
| Reversible | PASS (observability only; levers are follow-ups) |
| Every function has a test | PASS (test table above) |

## 11. Rollback criterion

Plan is rejected if: (a) the per-turn counters cannot be kept correct under the
planner path (before/after unavailable or mismatched); (b) the attribution is
not reliable (mode/load-lag/gap cannot be matched to a turn); (c) the session
counters race the loop under `go test -race` (sink on loop goroutine; counters
mutex-guarded). Rollback = revert the observability additions; levers never
ship without their own measurement first.
