# 51.08 - CLOSED (merged): a result-shaping stage

**Status:** **CLOSED - MERGED.** ADLC Step 0 ran 2026-08-03 and found the
seam already shipped. Superseded by
`.mivia/plans/archived/tools/06-aggregate-turn-byte-budget.md`, whose own
"Depends" note says its core was made pure "expressly so 51.08 can subsume
it without surgery". The subsumption already happened.
**Do not implement from this document.**
**Date:** 2026-08-02, closed 2026-08-03
**Part of:** program `51` (`00-overview.md`).

## 0. Step 0 disposition

Panel verdict: **DO-NOT-BUILD as written.**

| Finding | Severity | Disposition |
|---------|----------|-------------|
| §2's core claim "between the dispatcher and the append there is no transformation at all" is **false**: two exist - per-call cap+spool (`internal/agent/loop_scheduler.go:88`) and whole-batch shaping (`internal/agent/loop_tools.go:44` → `shape_batch.go:160`) | BLOCK | **Accepted.** The premise of the plan is gone. |
| §6.1, the plan's self-declared load-bearing decision (dispatcher vs loop), was already decided **in favour of the loop** and shipped | BLOCK | **Accepted.** The plan's central open question is closed by working code. |
| Delivery slice 1 ("seam with stages 3 and 4 wired") is already shipped | BLOCK | **Accepted.** Delete. |
| INV-CE-08-C ("no stage performs I/O") is already contradicted - shaping performs store **reads** (`shape_batch.go:132-141`, called from `:384`) | MEDIUM | **Accepted.** The invariant would forbid the shipped hybrid-retention design. Restate as "I/O confined to an injected, faultable `shapeEnv`". |
| INV-CE-08-D ("shaping never increases size") is **already false at HEAD** | MEDIUM | **Accepted as a shipped-code defect**, not a plan finding. Overview §8.5. |
| `toolResultBodyFailed` misclassification under a tier-3 degrade | LOW | **Accepted as a shipped-code defect.** Overview §8.6. |
| §4.2 `CondensingTool` has zero implementors, and its one named candidate is reachable without it now that `codeintel.FileOutline` is model-facing | MEDIUM | **Accepted.** Speculative generality; deleted. Let a tool shrink its own output (plan `03`); add an interface only on a second implementor. |
| Stage order dedup→condense→truncate is **unimplementable at the shipped seam** - truncation+spool run per-call in the worker, strictly before batch shaping | MEDIUM | **Accepted.** Dedup must hook `buildExecResult` (`loop_scheduler.go:52`) *before* `CapWithSpoolRef`. That is a different edit than the plan describes, and it is now plan `10`. |
| Stated dependency on `48` §3.1 | MEDIUM | **Rejected as inherited scope.** `48` items F and G are still TODO (`dispatcher.go:471` still destroys) and stay with `48`. |

## 1. What shipped

| Proposed stage | State at HEAD |
|----------------|---------------|
| 1. dedup | **Not shipped.** No seen-ledger exists anywhere in `internal/`. → plan `10` |
| 2. condense | **Not shipped**, and deleted as speculative |
| 3. truncate | `internal/agent/loop_scheduler.go:88`, `internal/remainder/notice.go:39,58` |
| 4. ceiling | `internal/runtime/output_ceiling.go:41,74,130`, enforced `dispatcher.go:471` (still destroy, not truncate - `48`-F) |
| The ordered, pure, testable seam | `internal/agent/shape_batch.go:160,192,220` |
| INV-CE-08-A honesty annotation | shipped |
| INV-CE-08-E default-off | `shape_batch.go:163-170` - `BatchResultBudgetBytes == 0` passes through verbatim |
| INV-CE-08-F content-free telemetry | shipped |

Five of the plan's six invariants were satisfied by code written
independently. Two of the remaining claims were false *about* that code, and
became defect reports.

## 2. Residual work, re-parented

| # | Item | Owner |
|---|------|-------|
| R1 | Seen-content substitution at insert time, hooked before `CapWithSpoolRef` | **Plan `10`** |
| R2 | Dispatcher truncate-instead-of-destroy | Plan `48` items F/G - **not this program** |
| R3 | `shape_batch` trailer can grow a result | Bug fix, overview §8.5 |
| R4 | Degraded `run_command` misclassified as success | Bug fix, overview §8.6 |

## 3. Why this document is kept

Two lessons worth more than the plan.

First, the plan's own §6.1 named the right question - "dispatcher or loop?"
- and marked it load-bearing. Independent work answered it the same way the
plan would have, which is weak evidence the question was well posed and
strong evidence that a plan left undelivered gets overtaken.

Second, the plan asserted invariants over code it had not re-read.
INV-CE-08-D was written as a requirement and turned out to be a description
of a bug. An invariant stated about existing code is a test that has not
been run yet.
