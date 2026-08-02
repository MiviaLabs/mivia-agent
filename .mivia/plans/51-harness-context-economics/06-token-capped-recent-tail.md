# 51.06 - Retire the message-count cap on the recent tail

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `51` (`00-overview.md`).
**Depends on:** nothing. Must not land concurrently with `49`, which edits
the same admission loop.
**Blast radius:** LOW - one loop in the planner, but it decides what the
model remembers, so the tests matter more than the diff.

## 1. Goal

Admit as much recent history as the token target allows, instead of stopping
at a fixed message count that is uncorrelated with cost.

## 2. Verified baseline

`retainMessages` (`internal/contextmgr/planner.go:166`) walks message units
newest-first and admits a unit only when **both** hold:

- `tailCount + len(unit) <= tailLimit`, where `tailLimit` defaults to
  `defaultRecentTailMessages = 8` and is bounded by
  `maxRecentTailMessages = 64` (`planner.go:17-18`);
- `runningCost + calibrated(unitTokens) <= target`, where `target` is half
  the budget (`percentFloor(budget, 1, 2)`).

The cost check `break`s (stops admission); the count check `continue`s.
Mandatory messages - system, current objective, latest tool unit - are
selected before the loop and are not subject to either cap.

## 3. The defect

The two caps are not two safety nets for the same hazard. `target` bounds
cost, which is the thing that actually matters. `tailLimit` bounds *count*,
which correlates with cost only when messages are uniformly sized - and in
an agent session they are emphatically not: a one-line acknowledgement and a
40 KiB grep result are both "one message".

The observable consequence: after compaction, a session whose recent history
is eight cheap messages retains eight cheap messages and leaves most of
`target` unspent. The model loses context it had budget for. The reverse
case - eight enormous messages - is already handled correctly by `target`,
which stops admission first.

So `tailLimit` never protects the budget. It only ever discards affordable
context.

## 4. Design

### 4.1 Primary change

Make `target` the sole admission bound for optional units. The loop keeps
its newest-first order and its `break`-on-cost behaviour, which already
guarantees termination and monotonic cost.

### 4.2 What replaces the count cap

Not nothing. Two residual concerns the count cap incidentally covered:

- **Unbounded message count.** A pathological history of thousands of
  near-empty messages could fit under `target` while producing a request
  with an absurd message count, which some providers bound independently of
  tokens. Retain `maxRecentTailMessages = 64` as a **hard structural
  ceiling**, and delete only the low default. The cap stops being the
  operative bound and becomes a backstop, which is what it should have been.
- **Determinism of the retained set.** Unchanged: admission remains
  newest-first with a deterministic break, so the idempotency key
  (`planIdempotencyKey`) stays stable for identical inputs.

`PlanInput.RecentTail` keeps its meaning for callers that set it explicitly;
only the default changes, from `8` to `maxRecentTailMessages`.

### 4.3 Interaction with `49`

Plan `49` replaces oversized tool-result bodies with a notice inside this
same selection. The two compose in one direction only: elision lowers unit
cost, so more units fit under `target`. Landing this plan first makes `49`'s
saving larger and its tests noisier. Landing `49` first is the safer order,
and this plan should state that dependency rather than race it.

## 5. Invariants

- **INV-CE-06-A.** Retained cost never exceeds `target` for optional units,
  and never exceeds `Budget` for the mandatory set. Unchanged from today;
  this plan must not weaken it.
- **INV-CE-06-B.** Admission is deterministic for identical `PlanInput`, so
  `IdempotencyKey` is unchanged for unchanged inputs.
- **INV-CE-06-C.** The retained set is a contiguous-by-unit suffix plus the
  mandatory set; tool pairing survives (`validateMessageShape`).
- **INV-CE-06-D.** The retained set under the new default is a superset of
  the retained set under the old default, for the same input. Strictly more
  context, never less.

## 6. Open decisions for Step 0

1. Is `maxRecentTailMessages = 64` still the right structural ceiling once
   it is the operative one for cheap histories? It was chosen as a bound on
   an explicitly configured value, not as a live limit.
2. Do any providers in `internal/provider` impose a message-count limit
   that this change could now reach? If yes, the ceiling belongs in the
   provider adapter, not the planner.
3. Does `Session.Compact` (`internal/chat/context_integration.go`), which
   supplies no tool schemas and therefore already mis-accounts, get
   materially worse under a larger tail? The mismatch is pre-existing and
   plan `49` §2 declines to conceal it; this plan should say the same
   explicitly rather than inherit it silently.

## 7. Delivery slices

1. Default `RecentTail` to `maxRecentTailMessages`; keep the ceiling.
2. Telemetry: retained message count and `AfterTokens` as a fraction of
   `target`, to show the unspent-budget defect closing.

## 8. Required tests

- Cheap history: nine one-line messages under `target` - all nine retained,
  where the old default retained eight.
- Expensive history: cost `break` fires before the ceiling; retained cost
  `<= target`.
- Superset property (INV-CE-06-D) over a table of histories.
- Explicit `RecentTail` still bounds admission exactly as before.
- `RecentTail` above `maxRecentTailMessages` still rejects with the existing
  `invalidPlan` error.
- Idempotency key unchanged for an input whose retained set is unchanged.

## 9. Out of scope

- Changing `target` or the 80% trigger. That is plan `43`.
- Any relevance-based (as opposed to recency-based) retention.
- Summarizing dropped units.
