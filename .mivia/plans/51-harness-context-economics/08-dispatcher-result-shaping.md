# 51.08 - A result-shaping stage in the dispatcher

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `51` (`00-overview.md`).
**Depends on:** `48` §3.1 (ceiling truncates instead of destroying) and `07`
(remainders are referenceable). Building shaping before `07` makes shaping
lossy, which INV-CE-C forbids.
**Blast radius:** HIGH - every tool result the model ever sees passes
through this stage.

## 1. Goal

Give the harness one ordered, testable place to reduce the cost of a tool
result before it becomes a message - instead of the current binary choice
between "pass it through" and "fail it for being too big".

## 2. Verified baseline

- The dispatcher computes a per-tool ceiling at registration
  (`toolOutputCeiling`), min'd against `Policy.MaxOutputBytes` in
  `effectiveCeilingLocked`, and enforces it in `reserve()`
  (`internal/runtime/output_ceiling.go`).
- Enforcement is a hard failure (`overCeilingError`), which the comment
  itself describes as the dispatcher's most confusing failure mode: "the
  tool did nothing wrong from its own point of view".
- Rule 10 is already respected here: the error names sizes and the
  capability, never content.
- After dispatch, `runToolBatch` appends each result verbatim as a
  `RoleTool` message (`internal/agent/loop_tools.go:48`). Between the
  dispatcher and that append there is no transformation at all.
- Per-tool budgets are declared by tools themselves via
  `tools.ResultBudgetTool` (`internal/tools/search.go:57`,
  `internal/tools/read.go:32`), so the harness already knows each tool's
  intended content size.

## 3. The defect

The dispatcher has exactly one lever - a size threshold - and one response -
refuse. Everything a harness could usefully do to a large result (drop what
the model already holds, keep the structurally informative part, page the
rest) is unavailable, so it lands in the system prompt as advice to the
model instead. That is precisely the "enforced at the prompt level" failure
this program exists to correct: the model can ignore advice, and pays
tokens to read it either way.

## 4. Design

### 4.1 The pipeline

One ordered stage between handler return and result delivery:

```
handler output
  -> 1. dedup      (substitute content the session has already delivered)
  -> 2. condense   (tool-declared structural reduction)
  -> 3. truncate   (48 §3.1 tail-truncation, spooled per 07)
  -> 4. ceiling    (runaway backstop only)
  -> result
```

Order is load-bearing and is itself an invariant (INV-CE-08-B): dedup before
condense, because condensing content that is about to be replaced by a
reference is wasted work and produces a different digest; truncate last,
because it is the only lossy step.

### 4.2 Stages are opt-in per tool, and tools own their semantics

Stage 1 and 3 are generic (bytes and digests). Stage 2 is not: only the tool
knows what "structurally reduce" means for its output. Expose it as an
optional interface alongside the existing `ResultBudgetTool`:

```go
type CondensingTool interface {
    Condense(result string, budget int) (string, bool)
}
```

A tool that does not implement it is passed through untouched, exactly as
today. This mirrors how `ResultBudgetTool` is already optional and how
`toolOutputCeiling` degrades to the floor for tools that declare nothing.

### 4.3 What the dedup stage is not

Stage 1 substitutes content in a result **being appended now**. It never
edits an existing message. Rewriting history invalidates the prompt cache
from that point forward (INV-CE-B), so retroactive dedup is forbidden here
and belongs only at a compaction boundary, which is plan `49`'s job.

The seen-ledger that stage 1 consults is specified in `03`.

### 4.4 Honesty

Every stage that changes a result annotates it. A shaped result must be
distinguishable from a naturally small one, both to the model and in the
audit trail. Silent shaping is the "tool-result truncation makes agents lie"
failure: the model reasons over a partial result believing it complete.

## 5. Invariants

- **INV-CE-08-A.** A result the model receives is either the handler's
  verbatim output, or is annotated with what was changed and how to recover
  it. No silent modification.
- **INV-CE-08-B.** Stage order is fixed: dedup, condense, truncate,
  ceiling. Tested directly, not implied by call order.
- **INV-CE-08-C.** Every stage is a pure function of (result, budget,
  ledger snapshot). No stage performs I/O beyond the content store write
  that `07` already owns.
- **INV-CE-08-D.** Shaping never increases result size. Each stage's output
  is `<=` its input, annotation included.
- **INV-CE-08-E.** Shaping is off by default for tools that declare
  nothing, and the default-config behaviour is byte-identical to `48`'s
  post-truncation behaviour.
- **INV-CE-08-F.** Rule 10 holds: dispatcher errors and telemetry carry
  sizes and names, never content.

## 6. Open decisions for Step 0

1. **Does shaping belong in the dispatcher or in the agent loop?** The
   dispatcher is capability-generic and already owns the ceiling; the loop
   is where the message is built and where the session ledger naturally
   lives. Putting session state into the dispatcher may be the wrong
   coupling - the dispatcher is currently free of per-session context.
   **This is the load-bearing decision of the plan** and it should be
   settled before anything else in it.
2. Does a shaped result change how `toolResultBodyFailed` classifies
   `run_command` output (`loop_tools.go`)? A truncated header could change
   failure detection.
3. Should `Condense` be able to *reorder* (rank-then-cut, per `03`), or
   only elide? Reordering is more valuable and much harder to test.
4. What does shaping mean for a parallel tool batch where two calls return
   the same content in the same batch? Order within the batch is already
   normalized by index sort; dedup must be deterministic under that order.

## 7. Delivery slices

1. The stage seam with only stage 3 and 4 wired (behaviour-identical to
   `48`). Proves the seam without changing output.
2. Stage 2 (`CondensingTool`), with `grep` as the first implementor via
   `03`.
3. Stage 1 (dedup), once `03`'s seen-ledger exists.

## 8. Required tests

- Default config, no declared budgets: output byte-identical to `48`'s.
- Stage-order test: a result that both dedups and condenses proves dedup
  ran first, by digest.
- Monotonic size (INV-CE-08-D) over a table of shaped results.
- Annotation present on every shaped result; absent on every unshaped one.
- Ceiling still fires as a runaway backstop after shaping.
- Parallel batch with two identical results: deterministic, index-ordered
  outcome.
- No content in any dispatcher error or telemetry field.

## 9. Out of scope

- Deciding *when* compaction runs (plans `43`, `49`).
- Any model call inside a stage. Shaping is deterministic host code.
- Changing `Capability` or the reservation protocol.
