# tools/06 - Aggregate per-turn tool-result budget

**Status:** DESIGN
**Date:** 2026-08-02
**Depends on:** `48` (truncate-not-destroy semantics), `51/07` +
`tools/01` (remainders referenceable - degrading to a ref requires the ref
to exist). Coordinates with `51/08` (result shaping) - if 51.08 ships, this
budget becomes one of its ordered shaping inputs rather than a separate
mechanism; check 51.08 status before implementing standalone.
**Blast radius:** MEDIUM - agent loop batch handling; no per-tool changes.

## 1. Problem

Every ceiling is per-call. N parallel `read_file`s each individually under
budget can jointly blow the context in one batch: 10 x 200 KiB results is
2 MB into history in a single step, forcing an immediate compaction that
evicts older context wholesale. Nothing bounds the *sum* across a tool
batch or a turn. (Uncapped-by-default per `48` makes this the only
remaining structural guard.)

## 2. Goal

A per-turn budget over accepted tool-result bytes, enforced by degrading
results to references - never by failing calls the model already paid for.

## 3. Design

### 3.1 Budget

- `[tools] turn_result_budget_bytes`, default derived: 25% of the effective
  prompt byte budget (from the calibrated token budget x ~4 bytes/token),
  floor 256 KiB. `0` = unlimited (consistent with `48`'s convention).
- Scope: one agent-loop turn (user message to final assistant message),
  reset at turn start. Subagent loops get their own instance.

### 3.2 Enforcement: degrade, don't destroy

Charged in the loop where `capToolResult` already runs, after per-tool
handling. When a result would exceed the remaining turn budget:

1. If remaining >= a floor (16 KiB): truncate to remaining with the honest
   notice + remainder ref (`tools/01` format) - the model gets a usable
   head plus a paging handle.
2. Below the floor: replace the body entirely with a stub
   `[result deferred: <tool>, N bytes, ref:output:...; turn result budget
   exhausted]`. The call itself still *succeeded* - tool side effects
   happened, status is intact; only the body transport is deferred.
3. Never reject or destroy: `48`'s reliability rule holds at the aggregate
   layer too.

Within a parallel batch, charge in deterministic call order (the order the
assistant issued them) so identical batches degrade identically -
resume/replay stable.

### 3.3 Model visibility

- After any degradation, the loop appends one bounded status line to the
  batch result: remaining turn budget and the count of deferred results -
  the model can choose to page refs or proceed.
- The budget is otherwise invisible (no per-turn noise when never hit).

### 3.4 What this is not

Not a cost-truth change: charged bytes are what actually entered history,
so `EstimateRequestCost`/calibration see reality unchanged. Not a
compaction substitute: it prevents single-turn blowouts; plan `49`/`51`
own steady-state growth.

## 4. Invariants

- Sum of tool-result bytes accepted into history per turn <= budget
  (+ notice/stub framing), when budget > 0.
- No tool call is failed or its side effects rolled back by this mechanism.
- Every deferred/truncated body is retrievable via its ref (depends on
  `tools/01`); the stub states total size.
- Deterministic under identical batches (ordering rule).
- Budget 0 -> mechanism fully inert.

## 5. Steps

1. Confirm 51.08 status; implement as a shaping input if it landed,
   standalone in the loop otherwise.
2. Budget config + derivation from prompt budget.
3. Charging + two-tier degradation in the loop; deterministic batch order.
4. Status line emission; event with per-turn totals for observability.
5. Docs.

## 6. Testing

- Batch matrix: all-fit / partial-truncate / stub-tier / mixed, asserting
  per-turn sum bound and ref resolvability.
- Determinism: same batch twice -> identical degradation.
- Interaction: degraded turn followed by compaction keeps pairing valid.
- Inertness at 0; floor boundary cases.

## 7. Failure analysis

- Model loops paging refs it could have avoided by narrower reads: status
  line names remaining budget, teaching narrower requests; observable via
  read_output call rates.
- Budget too small relative to a legitimately large single result: tier 1
  guarantees at least the floor of usable head plus full ref access.
