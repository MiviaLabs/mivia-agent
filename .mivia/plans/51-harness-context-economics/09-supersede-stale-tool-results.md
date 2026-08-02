# 51.09 - Drop superseded tool results for the same resource

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `51` (`00-overview.md`).
**Depends on:** `49` (elision tier) for the compaction-time replacement
mechanism, and `08` if any part runs at insert time.
**Blast radius:** MEDIUM - changes what history the model retains, which is
a quality question before it is a cost question.

## 1. Goal

When a later tool result describes the same resource as an earlier one, stop
carrying the earlier one. Superseded state is not merely expensive - it is
actively misleading.

## 2. Verified baseline

- Every tool declares a `Capability` carrying a `ResourceKey`, derived for
  filesystem tools by `pathCapabilityKey(args, ws)`
  (`internal/tools/search.go:49`, `internal/tools/read.go:26`). The harness
  therefore already knows *which resource* a call concerned.
- `markLatestToolUnit` marks only the most recent assistant tool-call unit
  as mandatory (`internal/contextmgr/planner.go:251`). Older tool units are
  optional and compete on cost alone in `retainMessages`.
- `messageUnits` groups an assistant tool-call message with its paired
  `RoleTool` results, so a unit is the correct granularity for
  replacement - dropping a result without its call breaks
  `ValidateToolPairing`.
- Plan `49` already specifies replacing an old oversized tool-result body
  with a host-authored notice while retaining the paired call and the
  existing unit selection. That is the mechanism this plan needs; this plan
  supplies a better *eligibility rule* than "oversized".

## 3. The rationale

The 2026 long-horizon tool-agent benchmark result is that pruning to recent
tool pairs **outperforms** full-context retention on task success, not just
on cost: older tool interactions describe superseded state, and retaining it
introduces noise that degrades the agent's model of the current system
state. ("Less Context, Better Agents", arXiv 2606.10209.)

The mivia-specific version: an agent reads `foo.go`, edits it, reads it
again. History now contains two contradictory full texts of `foo.go`, and
nothing in the prompt says which is current except message order. Recency is
a weak signal for a model reasoning over a long context, and the first copy
costs exactly as much as the second.

`retainMessages` currently keeps both if both fit under `target`. Cost-based
eviction is the wrong axis here: the older copy should go **even if it
fits**.

## 4. Design

### 4.1 Eligibility

A tool result is *superseded* when a **later** result in the same session
has the same `(tool class, ResourceKey)` and the earlier one is not in the
mandatory set. Superseded results are replaced by a notice - not deleted -
so pairing and unit structure survive, exactly as in `49`.

Three deliberate restrictions:

- **Class-scoped.** A `grep` over a directory does not supersede a
  `read_file` of a file inside it, even though the resource keys overlap.
  Only like supersedes like.
- **Never the mandatory set.** The latest tool unit and the current
  objective are untouchable, as today.
- **Mutation-agnostic.** The rule does not attempt to determine whether the
  resource actually changed. A re-read that returned identical bytes is
  handled by `03`'s digest dedup, which is a different and cheaper
  mechanism. This plan is about *possibly-stale* state; `03` is about
  *provably-identical* state.

### 4.2 Where it runs

At the compaction boundary, inside the planner, using `49`'s replacement
path. Not at insert time.

The reason is INV-CE-B: superseding an older message means editing history
that has already been sent, which invalidates the prompt cache from that
point. That cost is only worth paying at a moment when the prefix is being
rebuilt anyway - which is exactly what compaction is. A per-turn superseding
pass would reset the cache on most turns and could easily cost more than it
saves.

### 4.3 Write-tool interaction

An `edit`/`write_file` result supersedes prior *read* results for the same
path, in the sense that it proves the earlier text is stale. Whether the
harness should encode that cross-class relationship is an open decision
(§6.2); the safe default is no - class-scoped only, per §4.1.

### 4.4 Notice content

The notice must say *why* the body is gone and that a current copy is
obtainable, without asserting what the current content is. It must not claim
the resource changed - the harness does not know that.

## 5. Invariants

- **INV-CE-09-A.** The most recent result for any `(class, ResourceKey)` is
  never superseded. There is always exactly one live copy.
- **INV-CE-09-B.** Superseding replaces bodies only; tool calls, tool-call
  IDs, and unit structure are preserved and `ValidateToolPairing` still
  passes.
- **INV-CE-09-C.** Superseding runs only at a compaction boundary
  (INV-CE-B).
- **INV-CE-09-D.** The rule is a pure function of `(class, ResourceKey,
  order)`. It never inspects result content to decide eligibility.
- **INV-CE-09-E.** Determinism: identical `PlanInput` yields an identical
  superseded set, so `IdempotencyKey` remains stable.

## 6. Open decisions for Step 0

1. **Is `ResourceKey` actually stable enough?** `pathCapabilityKey` derives
   from tool arguments. Two reads of the same file with different
   `offset`/`limit` may or may not produce the same key - and if they do,
   this plan would supersede a window of a file with a *different,
   non-overlapping* window of the same file, destroying context the model
   still needs. **This is the plan's principal correctness risk** and must
   be resolved before anything is built. If keys are window-insensitive,
   the eligibility rule needs a span component.
2. Should write results supersede prior reads across classes (§4.3)?
3. Does superseding interact badly with `03`'s seen-ledger - can a result
   be both superseded and a dedup target, and does the order matter?
4. How is this evaluated? Cost is easy to measure; the claimed *quality*
   improvement is the actual justification and needs a task-level
   evaluation, not a token count. Without one, this plan is a cost
   optimisation wearing a correctness argument.

## 7. Delivery slices

1. Resolve §6.1 with a test over real tool arguments. If keys are
   window-insensitive, stop and redesign the key.
2. Eligibility rule as a pure function, tested standalone against
   synthetic histories, with no planner wiring.
3. Wire into `49`'s replacement path.
4. Evaluation harness for §6.4.

## 8. Required tests

- Two full reads of the same path: the earlier is superseded, the later is
  intact.
- Two *windowed* reads of disjoint ranges of the same path: **neither** is
  superseded (guards §6.1).
- A `grep` and a `read_file` touching the same path: neither supersedes the
  other.
- The latest tool unit is never superseded, even with a later duplicate in
  the same unit.
- Pairing validity after superseding, over a table of interleaved parallel
  batches.
- Idempotency key stability for identical inputs.
- No superseding occurs below the compaction threshold.

## 9. Out of scope

- Detecting whether a resource actually changed on disk.
- Digest-identical dedup (that is `03`).
- Summarizing superseded content instead of noticing it.
