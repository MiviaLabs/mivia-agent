# tools/06 - Aggregate per-turn tool-result budget

**Status:** DESIGN VALIDATED (2026-08-02) - baselines verified against HEAD;
decisions resolved below. Ready for ADLC Step 0 hostile challenge, then
implementation.
**Date:** 2026-08-02 (revised after code validation)
**Depends on:** `tools/01` read_output - **shipped** (`304f42d`:
`internal/remainder` spool, `internal/cli/read_output.go`, principal-scoped
grants, ref-bearing notices). NOT dependent on `48` (still DESIGN; see D4).
`51/08` result shaping is DESIGN/Step-0-not-run, so the standalone-in-loop
path applies; if 51.08 ships first, fold this in as a shaping input.
**Blast radius:** MEDIUM - agent loop batch append path, one new spool
call path, config; no per-tool changes.

## 1. Verified baseline

- **No aggregate accounting exists.** The only batch guard is count-based
  `MaxToolCallsPerBatch` (`internal/agent/loop_tools.go:204-211`) and it
  *fails* excess calls - the opposite of degrade-don't-fail. No byte
  counter with turn lifetime anywhere in `internal/agent`.
- **read_output machinery is real but narrower than assumed**: refs are
  minted only by loop-level `capToolResult` → `remainder.CapWithSpool`
  (`loop_limits.go:36-42`, called at `loop_tools.go:429`). `CapWithSpool`
  only spools when `maxBytes > 0 && len(result) > maxBytes`
  (`notice.go:28`) - a **full-body defer (zero bytes kept) is not
  expressible today** and needs a new spool call path.
- **Charging point correction**: `loop_tools.go:429` runs inside a
  concurrent worker (pool of `MaxConcurrentTools`, default 4); charging
  there is nondeterministic. Results are then **sorted by issue order**
  before append (`loop_tools.go:43-53`) - determinism already exists at
  the append loop, so the charge belongs there.
- Grants are principal-scoped by **SessionID** (`spool.go:58`,
  `read_output.go:112-124`); `MarkExpired` has **no production caller**
  and grants are in-memory process-local - a spooled body does not
  survive process restart.
- Dispatcher ceiling still destroys (`output_ceiling.go:67-68, 106-108`);
  plan 48 remains DESIGN.
- Defaults remain uncapped (`defaults.go:38-61`); no aggregate knob exists.
- Budget derivation source: `agent.Options.MaxContextTokens`
  (`loop.go:29-32`). **No precomputed byte budget exists**, and calibration
  is a ratio over *estimates* (`PlanInput.CalibrationRatio`,
  `planner.go:88`), not a budget - do not fold it into the byte budget.

## 2. Goal (unchanged)

A per-turn budget over accepted tool-result bytes, enforced by degrading
results to references - never by failing calls the model already paid for.
N parallel reads each under their own cap must not jointly blow the context
in one batch.

## 3. Resolved decisions

**D1 - charge at the ordered append loop** (`loop_tools.go:43-53`), not in
the workers. Workers run per-call capping exactly as today; the append loop
then charges each result in issue order against the turn counter and, when
over budget, runs a **second truncation pass** on that result. Deterministic
by construction (the sort already exists); the cost is re-truncating an
already-capped body - cheap, it is a string cut plus one spool call.

**D2 - new spool entry point for full deferral.** Add
`remainder.SpoolFull(body) -> ref` (mint a ref for the entire body, keep
zero bytes) alongside `CapWithSpool`, for the stub tier. Same INV: no ref
emitted unless the store write succeeded; notice falls back to ref-less
wording on store failure (`fitTruncation`'s existing never-partial-ref
guarantee extends to it).

**D3 - restart honesty in the stub.** Because grants are process-local and
`MarkExpired` is unwired, a deferred body is retrievable only within the
process lifetime. The stub says so
(`[result deferred: <tool>, N bytes, ref:output:...; valid this session]`),
and `read_output`'s existing `expired`/`not_found` statuses cover the
after-restart case. Wiring durable grants/retention is `51/07`'s remit,
not this plan's - but tier-2 deferral ships only *after* a decision that
this volatility is acceptable, taken at Step 0 challenge with the measured
frequency of tier-2 events (expected rare: tier 1 handles the common case).

**D4 - drop the dependency on 48.** Degraded results are *smaller*, so
this mechanism never pushes anything toward the destructive ceiling; the
two plans are orthogonal. The original claim that "48's rule holds at the
aggregate layer" is restated as this plan's own rule: the aggregate layer
never fails or destroys a call's result.

**D5 - subagent isolation check is a precondition.** Grants key on
SessionID; verify a spawned loop carries its own SessionID (its refs must
not be visible to sibling subagents of other sessions and vice versa).
If parent and child share a SessionID, that sharing is acceptable
(parent may read the child's deferred bodies - arguably a feature) but
must be a documented decision, not an accident.

## 4. Design

### 4.1 Budget

- `[tools] turn_result_budget_bytes`: `0` = unlimited = **default**
  (consistent with the uncapped-defaults decision in `48`).
- `-1` = derived: `MaxContextTokens x 4 bytes/token x 25%`, floor 256 KiB,
  computed once at turn start in the loop (nothing precomputes a byte
  budget - verified). Calibration ratio deliberately not applied (it
  corrects estimates, not budgets).
- Scope: one loop turn; counter reset where the turn starts. Each
  subagent loop instance gets its own counter (it is loop state, so this
  is automatic).

### 4.2 Enforcement at the append loop (per D1)

For each result in issue order:

1. Fits remaining budget -> append unchanged, charge actual bytes.
2. Over budget, remaining >= floor (16 KiB) -> second-pass truncate to
   remaining via `CapWithSpool` (existing path - ref + honest notice),
   charge what was kept.
3. Remaining < floor -> full defer via `SpoolFull` (D2): body replaced by
   the D3 stub, charge the stub size. Tool status and side effects
   untouched - the call succeeded; only body transport is deferred.
4. Never fail, never destroy (D4). `MaxToolCallsPerBatch` count guard is
   unchanged and out of scope.

After any degradation, append one bounded status line to the batch:
remaining turn budget + count of degraded results. Silent when never hit.

### 4.3 Not cost-truth changes

Charged bytes are what actually entered history; `EstimateRequestCost`
and calibration see reality unchanged. Compaction (plan 49, shipped)
owns steady-state growth; this bounds single-turn blowouts only.

## 5. Invariants

- With budget > 0: sum of tool-result bytes entering history per turn
  <= budget + bounded framing (stubs, notices, status line).
- No tool call failed or rolled back by this mechanism.
- Refs in stubs/notices resolve for the emitting principal at mint time
  (store-success-before-ref, extended to `SpoolFull`); post-restart
  irretrievability is stated in the stub (D3).
- Deterministic: identical batches degrade identically (issue-order
  charging at the sorted append loop).
- Budget 0 -> mechanism fully inert, zero new allocations on the hot path.

## 6. Implementation steps

1. D5 SessionID isolation check (read the code, document the finding in
   this plan before building).
2. `remainder.SpoolFull` + notice/stub formats + tests (incl. store-failure
   fallback).
3. Turn counter + derivation in the loop; config knob (0 default, -1
   derived, explicit positive).
4. Append-loop charging + two-tier degradation + status line.
5. Observability: per-turn totals + degraded counts in the existing event
   vocabulary (`KindTokenUsage`-adjacent, content-free).
6. Docs.

## 7. Testing

- Batch matrix: all-fit / tier-1 / tier-2 / mixed, asserting the per-turn
  sum bound and ref resolvability via read_output round-trip.
- Determinism: same batch twice -> byte-identical degradation.
- Second-pass truncation composes with per-call caps (notice from pass 1
  survives or is superseded coherently - define which in tests).
- Compaction after a degraded turn keeps tool pairing valid.
- Inertness at 0 (allocation benchmark); floor boundary; derived-budget
  computation with/without MaxContextTokens set.
- Restart: tier-2 stub then process restart -> read_output returns
  expired/not_found, never bytes of another principal.

## 8. Failure analysis

- Model pages refs it could avoid with narrower reads: status line names
  remaining budget; observable via read_output call-rate events.
- Legitimately large single result vs small budget: tier 1 guarantees the
  16 KiB floor of head plus full ref access.
- Tier-2 body lost to restart: stated in the stub (D3); if measurement at
  Step 0 shows tier-2 is common rather than rare, block on 51/07 durable
  retention instead of shipping volatile deferral.
