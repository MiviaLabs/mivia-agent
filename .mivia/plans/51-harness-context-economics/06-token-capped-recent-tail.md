# 51.06 - Retire the message-count cap on the recent tail

**Status:** DESIGN LOCKED - ADLC Step 0 complete (2026-08-03). Ready for
Step 1 micro-task breakdown.
**Date:** 2026-08-02, revised after challenge 2026-08-03
**Part of:** program `51` (`00-overview.md`).
**Depends on:** no code dependency. **Sequence: implement after `05`** - see
§0 F8. The `49` conflict is moot: `49` is shipped and archived.
**Blast radius:**
- **Product:** LOW-MEDIUM. It changes which history the model retains after
  compaction, and admits a class of message unit that is structurally
  unadmittable today.
- **Engineering:** LOW. One loop in the planner, one default constant.

## 0. Step 0 disposition (hostile challenge)

Panel verdict: **PARTIAL.** The change is correct and worth making; two of
its invariants were wrong and its central argument was wrong in kind.

| Finding | Severity | Disposition |
|---------|----------|-------------|
| INV-CE-06-D (new retained set ⊇ old) is **false**. Counterexample: newest optional unit `U_a` = assistant + 8 tool results (9 msgs), older `U_b` = 1 cheap msg. Today `0+9 > 8` skips `U_a` and admits `U_b`; with the new default `U_a` is admitted, then `runningCost + c_b > target` **breaks** and `U_b` is dropped | BLOCK | **Accepted.** Invariant replaced, §5. |
| §3's claim "`tailLimit` never protects the budget, it only discards affordable context" is **wrong in kind**. Units are atomic (`planner.go:190-214`) and `loop_tools.go:45-51` appends one `RoleTool` per parallel call, so **any batch unit of ≥8 messages is structurally unadmittable as optional context today**. The change admits a whole class of unit that never entered the tail | BLOCK | **Accepted.** The defect is restated in §3: the count cap makes admission non-contiguous and size-discriminatory. |
| INV-CE-06-C described today's retained set as "a contiguous-by-unit suffix". **False today** - the count check `continue`s (`planner.go:162`), so an oversized-by-count unit is skipped and an older one admitted | MEDIUM | **Accepted.** Contiguity is *established* by this change, not preserved. That is the change's best argument and it was buried. |
| §6.3: the `Session.Compact` accounting mismatch gets **materially worse**, not neutral. The manager is built with no `Tools` (`internal/cli/context_setup.go:102`, `internal/chat/context_integration.go:467` vs `:384`), and `planCompact` deliberately skips a post-retention budget re-check (`planner_elision.go:32-34`). Today's 8-message cap masks it by keeping the tail far below `target` | MEDIUM | **Accepted as a precondition.** See §4.3. |
| §6.2: **no** provider imposes a message-count or per-message limit. `zai.go`, `deepseek.go`, `openrouter.go` are thin `OpenAICompat` constructors; `openai_compat.go:413-433` and `api_message.go:32` do a pure shape map with no count validation | LOW | **Accepted.** The ceiling has no provider justification; §4.2 restates what it is for. |
| Pairing survives (units are admitted whole and `ValidateToolPairing` forces batch results consecutive), but the failure mode is now **reachable and untested** - §8 had no parallel-batch test, and the change's largest delta is exactly large batch units | MEDIUM | **Accepted.** Required test added, §8. |
| Test churn is near-zero; **no test encodes 8 as product behaviour**. `planner_retention_test.go:25` uses `maxRecentTailMessages+1`; `planner_test.go:86` sets `RecentTail: 2`; elision tests pin `RecentTail: 64`; no non-test caller sets `RecentTail` | LOW | **Accepted.** The risk is missing coverage, not migration cost. |
| The gain may not materialise: `runningCost` is **seeded with the mandatory set plus the full tool-schema cost** (`planner.go:144,158`). If that already meets `target`, the first optional unit breaks and no tail is admitted whatever `tailLimit` says | MEDIUM | **Accepted.** This is why `05` sequences first. |
| Baseline errors: `retainMessages` is at `planner.go:136` not `:166`; `49` is shipped and archived, so the "must not land concurrently" constraint is moot; the mandatory-set hard-budget rejection (`planner.go:148-150`) is a third cap the plan omitted | Confirmed | Corrected in §2. |

### Locked thesis

The message-count cap is not a weaker duplicate of the token cap. It is a
**different and wrong selection rule**: because it `continue`s rather than
`break`s, it silently skips large units and admits older, smaller ones,
producing a non-contiguous retained set that discriminates by message count
rather than by cost or recency. Removing it makes retention contiguous,
recency-ordered, and bounded by cost alone.

The original "strictly more context" framing was wrong and is retracted.

## 1. Goal

Make the optional retained set the longest affordable unit-suffix of
history under `target`, rather than a non-contiguous selection filtered by
an unrelated message count.

## 2. Verified baseline (re-read at Step 0)

`retainMessages` (`internal/contextmgr/planner.go:136`) walks message units
newest-first. Three caps, not two:

1. **Mandatory-set hard budget** (`planner.go:148-150`): the mandatory set
   alone exceeding `Budget` rejects the plan.
2. **Count cap** (`planner.go:151-163`): `tailCount + len(unit) > tailLimit`
   → **`continue`** (skip this unit, keep scanning older ones).
   `defaultRecentTailMessages = 8`, `maxRecentTailMessages = 64`.
3. **Cost cap** (`planner.go:171-174`): `runningCost + calibrated(unit)
   > target` → **`break`**.

`runningCost` is seeded at `planner.go:144` with the mandatory set **plus
the full tool-schema cost** (`costForSelected` → `EstimatePromptCost(...,
input.Tools)`, `planner.go:158`). `target` is therefore a shared bound
across mandatory, schemas, and tail - not a tail allowance.

`retainMessages` is reached only from `planCompact` (`planner_elision.go:23`);
below the trigger `Plan` returns its input untouched (`planner.go:100`).

Units are atomic: an assistant message plus all its paired `RoleTool`
results (`planner.go:190-214`), and a parallel batch appends one `RoleTool`
per call (`internal/agent/loop_tools.go:45-51`), so unit size is
model-controlled and routinely exceeds 8.

## 3. The defect (restated)

Two distinct problems, only one of which the original draft named.

**3.1 Non-contiguous selection.** The count check `continue`s. A unit too
large by count is skipped and scanning proceeds to older units, so the
retained optional set is not a suffix of history. The model receives a
history with a hole in it, and the hole is chosen by message count.

**3.2 Structural exclusion of large units.** Any unit of ≥ `tailLimit`
messages can never be admitted as optional context at all. With the default
of 8, every parallel tool batch of 8 or more calls is permanently
unadmittable - and batch size is chosen by the model, not by the operator.

The original claim, that the count cap only ever discards affordable
context, was false: it also changes *which* context is selected.

## 4. Locked design

### 4.1 Primary change

Default `RecentTail` to `maxRecentTailMessages` (64) so the cost cap is the
operative bound for optional units. The loop keeps its newest-first order
and its `break`-on-cost behaviour, which already guarantees termination and
monotonic cost. With the count cap no longer binding in practice, the only
remaining skips are already-selected units, so the optional set becomes a
contiguous unit-suffix.

`PlanInput.RecentTail` keeps its meaning for explicit callers; only the
default changes.

### 4.2 What the ceiling is for

`maxRecentTailMessages = 64` stays, but its justification is corrected: it
is **not** a provider constraint (none exists, §0) and **not** a budget
guard (`target` is). It is a determinism and latency bound on the retain
loop and on request message count. Documented as such, or deleted - see
§6.2.

### 4.3 Precondition: the `/compact` schema mismatch

Under `/compact` the manager is built without `Tools`, so the tail now fills
to `target` with schema mass uncounted, and `planCompact` performs no
post-retention budget re-check. Today's 8-message cap masks this by keeping
the tail far below `target`.

**This plan does not ship until one of the following holds:** the Compact
path passes tool specs, or `planCompact` re-checks `after <= Budget`. This
is a precondition, not a bundled deliverable, and it is recorded as overview
defect §8.8.

## 5. Invariants

- **INV-CE-06-A.** Retained cost never exceeds `target` for optional units
  and never exceeds `Budget` for the mandatory set - **given
  `PlanInput.Tools` reflects the tools actually sent.** The qualifier is
  load-bearing; see §4.3.
- **INV-CE-06-B.** Admission is deterministic for identical `PlanInput`, so
  `IdempotencyKey` is unchanged for unchanged inputs.
- **INV-CE-06-C** (restated). *After* this change the optional retained set
  is a contiguous unit-suffix ending at the newest unit. **Today it is
  not.** Tool pairing survives because units are admitted whole.
- **INV-CE-06-D** (restated, replacing the false superset claim). The
  retained optional set is the longest unit-suffix affordable under
  `target`. For any history in which every unit is no longer than the old
  default, it is a superset of the old set; **in general it is not**, and
  may drop older cheap units in favour of newer large ones. That is the
  intended recency semantics.

## 6. Closed decisions (were open)

| # | Decision | Lock |
|---|----------|------|
| 1 | Is 64 still the right ceiling once operative? | Keep, but re-justify as a determinism/latency bound, not a provider or budget one |
| 2 | Do providers impose message-count limits? | **No.** Verified across every adapter |
| 3 | Does `Session.Compact` get worse? | **Yes, materially.** Precondition §4.3 |
| 4 | Conflict with `49`? | **Moot.** `49` is shipped and archived; its elision runs before retention |
| 5 | Sequence vs `05` | **After `05`.** Schema mass is seeded into `runningCost`; until it is auth-scoped the gain may be invisible |

## 7. Delivery slices

1. Precondition §4.3 - tool specs on the Compact path, or a `planCompact`
   budget re-check. RED test first.
2. RED tests for the restated INV-CE-06-C and -D, including the
   parallel-batch case.
3. Default `RecentTail` to `maxRecentTailMessages`; keep the ceiling and
   correct its doc comment.
4. Telemetry: retained message count, `AfterTokens` as a fraction of
   `target`, **and** mandatory+schema cost as a fraction of `target` - the
   last one is what shows whether the gain is reachable at all (§0 F8).

## 8. Required tests

| Test | Asserts |
|------|---------|
| Cheap history of 9 one-line units - all 9 retained where the old default retained 8 | the simple gain |
| Non-contiguity is gone: a history where a large-by-count unit is followed by cheap older units admits the suffix, not the hole | INV-CE-06-C |
| Counterexample from §0 F1 (9-message unit then a cheap older unit) retains the newer and drops the older | INV-CE-06-D, and proves the old superset claim false |
| **Parallel batch of ≥9 tool results admitted as optional context** | pairing survives, unit admitted whole |
| Cost `break` fires before the ceiling; retained cost ≤ `target` | INV-CE-06-A |
| Explicit `RecentTail` still bounds admission as before; `> maxRecentTailMessages` still rejects | no regression on the configured path |
| Idempotency key unchanged for an input whose retained set is unchanged | INV-CE-06-B |
| `/compact` path: retained cost + schema cost ≤ `Budget` | §4.3 precondition |

## 9. Plan scorecard

| Criterion | Result |
|-----------|--------|
| Compiles against current architecture | PASS |
| No new types or packages | PASS |
| Testable without network | PASS |
| Invariants match observed behaviour | PASS (after restatement) |
| Precondition explicitly gated, not bundled | PASS |
| Sequencing specified | PASS |
| Rollback criterion | PASS |

## 10. Rollback criterion

Revert if a compacted request exceeds `Budget` on any path, or if pairing
validation fails on a retained set containing a large parallel batch.

## 11. Residual risk

- The gain is bounded by schema mass seeded into `runningCost`. On a
  full-catalogue root agent it may be near zero until `05` lands. The
  telemetry in slice 4 is what makes that visible rather than assumed.
- The ceiling at 64 remains arbitrary. It is now honestly labelled as such.

## 12. Out of scope

- Changing `target` or the 80% trigger (plan `43`).
- Relevance-based retention.
- Summarizing dropped units.
