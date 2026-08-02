# tools/06 - Aggregate per-batch tool-result budget

**Status:** DESIGN LOCKED - ADLC Step 0 complete: round 1 REWORK applied,
round 2 re-challenge verdict **LOCK with amendments** (2026-08-02), all six
round-2 amendments applied below. Ready for implementation.
**Date:** 2026-08-02 (revised after Step 0 rounds 1+2)
**Depends on:** `tools/01` read_output (shipped, `304f42d`). NOT dependent
on `48` (D4, survived challenge). `51/08` result shaping: the enforcement
core is now a pure function (D6) expressly so 51.08 can subsume it without
surgery.
**Blast radius:** MEDIUM - `toolExecResult` becomes structured, append-loop
shaping, config; no per-tool changes.

## 0. Step 0 disposition (hostile challenge)

- **S1**: "turn" was undefined - per-Run charging would bound bytes
  compaction had already evicted. -> **scope = one `runToolBatch`** (D7),
  matching the plan's own goal sentence; renamed the plan accordingly.
- **S2**: welded-in shaping would be throwaway when 51.08 lands. ->
  enforcement is a pure `shapeBatch(results, budget) -> shaped, report`
  function; the append loop only invokes it (D6).
- **S3**: a free-floating status line has no legal message shape (orphan
  RoleTool message; `RepairToolPairing` would discard it). -> status text
  is appended **inside the body of the last degraded result**, charged as
  framing (D8).
- **S4**: tier-2 ship-gate was circular (measure a mechanism that doesn't
  exist) and its payoff <= 16 KiB/result. -> **tier 2 cut from v1**; D2
  (`SpoolFull`) and D3 (volatility stub) **deleted**. Below the floor,
  truncate to the floor and accept bounded overshoot.
- **C1 (BLOCKER)**: second-pass `CapWithSpool` on the already-capped
  string spooled the wrong bytes, could clip the embedded pass-1 ref
  mid-token, and lost the true total. -> workers pass **structured
  parts** through `toolExecResult`; pass 2 re-cuts the **original** body
  and emits one coherent notice/ref (D9).
- **C2**: subsumed by the tier-2 cut + D9.
- **C3**: ephemeral results must be charged but never spooled - a spooled
  ref would let the model resurrect scrubbed bodies via `read_output`. ->
  ref-less degrade for `EphemeralResultTool` results (D10).
- **C4**: hook context is appended after capping by design; a flat-string
  second pass would cut or mischarge it. -> hook context travels as a
  separate part, re-appended after shaping, charged as framing (D9).
- **C5**: derived budget with `MaxContextTokens <= 0` -> mechanism inert;
  floor applies only to a positive derivation.
- **C6/C7**: framing bound now explicit (per-batch scope + notice sizes);
  `errorResults` from `processToolCalls` are exempt (pre-batch, bounded),
  worker-synthesized error bodies are charged - both stated.
- Survived: D4 (no dependency on 48), D5 (SessionID isolation
  precondition), determinism of append-loop charging, spool grant
  concurrency (mutex-guarded, dedup benign).

### Round 2 (verdict: LOCK with amendments, all applied)

- **F1/F2**: the "identical to today's peak" memory claim was false
  (batch-scaled retention vs today's concurrency-scaled transient); the
  "originalBody or (refA,totalN)" alternative resolved to the **hybrid**:
  untruncated results carry the shared string, truncated results carry
  `(refA, totalN)` only; pass 2 `Spool.Load`s refA (read path available:
  loop holds `RemainderSpool` + `SessionID`), never re-stores; Load
  failure -> keep the pass-1 string whole and charge it.
- **F3 (HIGH)**: pass-2 re-cut to `max(remaining, floor)` could EXCEED a
  tool's per-call cap (4 KiB-cap tool inflated to 16 KiB). Target is now
  `min(pass1EffectiveCap, max(remaining, floor))`. And the status line is
  **reserved inside** the pass-2 envelope of the last degraded result,
  never inserted post-hoc.
- **F4**: determinism invariant qualified to "given identical store
  outcomes"; store failure on result i affects only result i.
- **F5**: budget check uses body bytes only; hook bytes move to the
  framing bound `(noticeMax + statusLineMax + MaxHookContextBytes) x N`.
- **F6 (MEDIUM-HIGH)**: overshoot bound was vacuous with
  `MaxToolCallsPerBatch = 0`. Floor applies only while `remaining > 0`;
  after exhaustion results degrade to **notice-only** (~100 bytes, ref +
  true total; ref-less for ephemeral). Overshoot bound becomes
  `budget + floor + framing`, independent of batch size.

## 1. Verified baseline

- No aggregate accounting exists; the only batch guard is count-based
  `MaxToolCallsPerBatch` (`loop_tools.go:204-211`) and it fails calls.
- `capToolResult` -> `remainder.CapWithSpool` (`loop_limits.go:36-42`)
  runs inside concurrent workers (`loop_tools.go:429`); results are
  sorted by issue order before append (`loop_tools.go:43-53`).
- `CapWithSpool` spools its input and only when over cap
  (`notice.go:28,34`); `fitTruncation` never emits a partial ref for its
  own notice (`notice.go:43-44`) but cannot protect an embedded one.
- Hook context is appended after capping, deliberately outside the tool
  budget (`loop_tools.go:430-434`; `MaxHookContextBytes` 8 KiB).
- Ephemeral bodies persist until `ScrubEphemeralToolMessages` after the
  final step (`loop_tools.go:436-439, 459-485`).
- `MaxContextTokens: 0` = no pruning (`loop.go:29-32`); no byte budget is
  precomputed anywhere; calibration is an estimate ratio, not a budget.
- `read_output` grants are SessionID-scoped, mutex-guarded
  (`spool.go:57-95`, `read_output.go:116-124`).

## 2. Goal

N parallel tool calls, each under its own cap, must not jointly blow the
context in one batch - enforced by degrading results to references, never
by failing calls the model already paid for.

## 3. Locked decisions

**D1 (revised) - charge at the ordered append loop via a pure shaper.**
Workers cap per-call exactly as today but return structured parts (D9).
The append loop calls `shapeBatch` (D6) over the issue-ordered results;
deterministic by construction.

**D4 - no dependency on plan 48.** Degraded results are smaller; the
aggregate layer never fails or destroys a call's result - its own rule,
independent of the dispatcher ceiling's fate.

**D5 - SessionID isolation check is implementation step 1**: verify a
spawned loop's refs are invisible across sessions; parent/child sharing
within one SessionID, if found, is documented as intended.

**D6 - pure enforcement core.** `shapeBatch(parts []resultParts, budget
int) (shaped []string, report shapeReport)` - no loop state, no I/O
except spool writes through an injected interface; unit-testable alone;
callable later from a 51.08 dispatcher-level shaper.

**D7 - scope = one tool batch.** Counter resets per `runToolBatch`. The
compaction interaction vanishes (nothing is charged across prune
boundaries); multi-batch turns are bounded per batch, and cross-batch
growth remains compaction's job (plan 49, shipped).

**D8 (amended F3) - status text is composed into the pass-2 envelope**:
one bounded line - remaining batch budget + degraded count - built by
`shapeBatch` as `content + status + notice` in one envelope for the last
degraded result, with `statusLineMax` reserved out of the re-cut target.
Body <= target holds by construction; no post-hoc insertion into a built
body (the C1 failure class). No new message, no pairing hazard. Silent
when nothing degrades.

**D9 (amended F1/F2) - structured `toolExecResult`, hybrid retention.**
Workers return `{cappedBody, refA?, totalN, effectiveCap, hookContext,
ephemeral}`. Untruncated results: `cappedBody` IS the original (shared
string, zero extra bytes). Truncated results: pass 1 already spooled the
original - carry `(refA, totalN)` only (~60 bytes), never the body.
Pass 2 on a truncated result `Spool.Load`s refA (same principal, cheap,
slow-path-only) and re-cuts the ORIGINAL, emitting ONE notice naming refA
with the true total - no re-store, so no pass-2 store-failure downgrade
for previously truncated results. Load failure: keep the pass-1 string
whole and charge it (bounded overshoot <= per-call cap; never
string-surgery the pass-1 body). Pass-2 re-cut target =
`min(effectiveCap, max(remaining, floor))` - shaping never inflates a
result past its per-call capability contract (F3). Hook context is
re-appended after shaping, outside the budget check, inside the framing
bound (F5).

**D10 - ephemeral results are charged, never spooled.** Degrade for
`EphemeralResultTool` results is ref-less truncation (plain notice, no
remainder ref), so `ScrubEphemeralToolMessages`' contract holds - nothing
scrubbed is resurrectable via `read_output`.

## 4. Design

### 4.1 Budget

- `[tools] batch_result_budget_bytes`: `0` = unlimited = default.
- `-1` = derived: `MaxContextTokens x 4 x 25%`, floor 256 KiB, computed
  at batch start. **If `MaxContextTokens <= 0`, derived mode is inert**
  (C5); the floor applies only to a positive derivation. Calibration
  ratio deliberately not applied.
- Scope: one `runToolBatch` (D7). Subagent loops get their own counter
  automatically (loop state).

### 4.2 Enforcement (`shapeBatch`, issue order)

1. Fits remaining -> unchanged; charge **body bytes only**
   (`len(cappedBody)`); hook bytes are framing, not budget (F5).
2. Over budget with `remaining > 0` -> re-cut ORIGINAL (via the D9 hybrid:
   in-hand string or `Spool.Load(refA)`) to
   `min(effectiveCap, max(remaining, floor 16 KiB))`, ref-less for
   ephemeral (D10); one coherent notice with true totals; charge kept +
   notice. Floor-below-remaining is deliberate bounded overshoot (S4) -
   at most ONE straddling result pays it (F6).
3. Over budget with `remaining == 0` -> **notice-only degrade** (F6):
   body replaced by the ref + true-total notice (~100 bytes; ref-less for
   ephemeral). Full bodies stay behind refs; nothing destroyed.
4. Last degraded result's envelope reserves and carries the D8 status
   line.
5. Never fail, never destroy; tool status and side effects untouched.
   `errorResults` (`loop.go:339`) are pre-batch and exempt;
   worker-synthesized error bodies pass through shaping and are charged
   (C7).

### 4.3 Cost truth

Charged bytes are what entered history via the budget term; hook context
and notices enter via the framing term - both visible to estimates and
calibration. Framing bound (C6/F5):
`(noticeMax + statusLineMax + MaxHookContextBytes) x batchSize`.

## 5. Invariants

- With budget > 0: bytes entering history per batch <=
  `budget + floor (one straddling result) + framing bound` - finite
  regardless of batch size, including `MaxToolCallsPerBatch = 0` (F6).
- No tool call failed or rolled back; side effects intact.
- Every emitted remainder ref resolves for the emitting principal; no ref
  ever points at a truncation artifact (D9 - refs always cover the
  original body's remainder); ephemeral bodies never behind a ref (D10).
- Deterministic **given identical store outcomes** (F4): a store failure
  on result i affects only result i's notice (plain, ref-less) and its
  charge; per-result independence, no rollback. With the D9 hybrid,
  pass 2 performs no writes for previously truncated results, shrinking
  this surface to fresh pass-2 truncations only.
- Shaping never inflates a result past its per-call capability contract
  (F3: `min(effectiveCap, ...)` clamp).
- Budget 0 -> `shapeBatch` not invoked; zero allocations on the hot path.
- Hook context never cut by shaping and never depletes the budget (C4/F5
  preserved).

## 6. Implementation steps

1. D5 SessionID isolation check; document finding here.
2. `toolExecResult` -> structured parts (D9); workers populate; append
   loop reassembles unshaped path byte-identically (golden).
3. `shapeBatch` pure function + unit tests (D6): tiers, floor overshoot,
   ephemeral ref-less path, hook re-append, status line, true-total
   notices.
4. Loop wiring: budget knob (0/-1/positive), per-batch counter, charge at
   append.
5. Observability: per-batch totals + degraded counts, content-free event.
6. Docs.

## 7. Testing

- Batch matrix: all-fit / degrade-one / degrade-many / floor-straddle /
  notice-only tail (remaining==0) / huge batch with
  MaxToolCallsPerBatch=0 asserting the F6 finite bound.
- D9 composition: pass-1-capped result re-shaped via Spool.Load -> single
  notice, true total N, ref pages the original remainder byte-identically
  (read_output round-trip); no partial-ref substring anywhere in shaped
  output (regex guard test); Load-failure fallback keeps pass-1 string
  whole; small-cap tool never inflated past effectiveCap (F3).
- Ephemeral: degraded ephemeral result has no ref; post-scrub
  `read_output` of any prior ref from that result fails.
- Hook context: present after shaping, uncut, charged as framing.
- Determinism: same batch twice -> byte-identical output.
- Inertness at 0 (allocation benchmark); derived-mode with
  MaxContextTokens 0/unset -> inert; floor boundaries.
- Compaction after a degraded batch keeps pairing valid.

## 8. Failure analysis

- Model pages refs it could avoid: status line names remaining budget;
  read_output call-rate observable.
- Floor overshoot: at most one straddling result pays the floor; the
  notice-only tail bounds everything after, batch size irrelevant (F6).
- Memory: untruncated results share the existing string; truncated
  results hold ~60 bytes of ref metadata - no batch-scaled retention of
  originals (F1/F2 hybrid).
- 51.08 lands later: it absorbs `shapeBatch` as its first stage (D6);
  the append-loop call site is the only discarded code.
