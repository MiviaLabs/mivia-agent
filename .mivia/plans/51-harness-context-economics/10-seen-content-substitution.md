# 51.10 - Seen-content substitution at tool-result insert time

**Status:** DESIGN - **BLOCKED.** Created 2026-08-03 during Step 0 by
re-parenting the seen-ledger from `03` and the residual dedup stage from
`08`, which described the same feature from opposite ends. It is blocked on
a shipped-code defect (§3) and must not be built until that is fixed.
**Date:** 2026-08-03
**Part of:** program `51` (`00-overview.md`).
**Depends on:** overview defect §8.2 (elision notices carry no recoverable
reference) being fixed. Hard blocker, not a sequencing preference.
**Blast radius:** MEDIUM - tool-result content at insert time, plus one new
piece of per-session state.

## 1. Why this plan exists

Two members proposed the same mechanism from different ends:

- `03` §4.3 specified a seen-ledger keyed by `contentref` digest, consulted
  when a search result is delivered.
- `08` §4.1 specified "stage 1, dedup", consulted when any tool result is
  delivered.

Both were challenged at Step 0; both halves were removed from their parent
plans and merged here so the feature has **one owner and one invariant
set**. Neither parent retains any dedup scope.

## 2. Goal

When a tool result about to be appended is byte-identical to content the
model already holds, substitute a reference instead of re-delivering it.

## 3. The blocker

A ledger that says "identical content shown at step 14" is a **factual
claim about the model's context**. At HEAD that claim can be false.

Shipped compaction elision (`internal/contextmgr/planner_elision.go:98-124`)
replaces every non-mandatory prior tool body over 2048 B with a notice
carrying **no reference at all** (`elisionNotice`, `:127-131` - it
deliberately excludes digests, tool names and arguments), and
`l.Messages` is durably replaced (`internal/agent/context.go:47`).

So after one compaction, content the ledger believes was delivered is both
absent from context and **unrecoverable**. Substituting a reference for it
would violate INV-CE-C and would put a false statement in front of the
model.

**Precondition P1:** elision notices must carry a recoverable `contentref`
handle before any ledger is built. That is overview defect §8.2 and plan
`09` salvage item S2. Until P1 lands, this plan does not start.

## 4. Design

### 4.1 The ledger is derived, never accumulated

The panel's structural finding: no correct invalidation rule is expressible
at the dispatcher, which holds no session state, and `Plan` returns no
per-message dropped/elided set a ledger could consume - only the aggregates
`ElidedMessages`/`ElidedBytes` (`planner.go:56-57`).

Therefore the ledger lives in the **loop**, beside `l.Messages`, and is
**rebuilt from the retained set after every `Prepare`**. "Seen" means
*derivable from the history the model currently holds*, not *ever
delivered*. An accumulated ledger cannot be made correct; a derived one is
correct by construction.

### 4.2 Where substitution hooks

Before `CapWithSpoolRef`, in `buildExecResult`
(`internal/agent/loop_scheduler.go:52`) - **not** inside `shapeBatch`.

The shipped pipeline truncates and spools per call in the worker, strictly
before batch shaping (`internal/agent/loop_tools.go:44`). A dedup stage
placed in `shapeBatch` would run after the bytes were already cut and
spooled, so it would hash a truncated body and never match the full one it
was meant to deduplicate.

### 4.3 Scope: which tools

**Whole-result bodies of `read_file` and `run_command` only.**

`grep` is **excluded**. A `contentref` is 75 bytes
(`contentref.go:47`); a grep match line is typically 40-80 bytes, so
per-match substitution would *increase* size. At whole-result granularity
two greps are byte-identical only in the degenerate repeat case, and once
`03` Stage A adds structure fields and Stage B adds rank order, even that
becomes rare.

This is a real narrowing of the original ambition and it is the honest
scope: dedup pays only where the payload dwarfs the reference.

### 4.4 Keying

`contentref.Reference` over the result body, exactly as minted today. No
private hash, no `(path, span)` tuple - `contentref.Reference(kind, data)`
takes only `data` (`contentref.go:42`), and inventing a tuple key would
create a second canonicalisation the package exists to prevent.

## 5. Invariants

- **INV-CE-10-A.** A substitution is emitted only when its referent is
  present in the current retained message set, or is resolvable through a
  spooled reference. The ledger is rebuilt from the retained set after
  every preparation.
- **INV-CE-10-B.** Substitution happens at insert time only. History
  already sent is never rewritten (INV-CE-B).
- **INV-CE-10-C.** A substitution never increases result size. Enforced by
  comparing against the body it replaces, **trailer included** - the
  shipped `shape_batch` shrink guard omits the trailer and is wrong for
  exactly this reason (overview §8.5).
- **INV-CE-10-D.** A substituted body is marked non-authoritative, and its
  referent is excluded from elision and from any future superseding. Two
  mechanisms must never leave zero live copies of a resource.
- **INV-CE-10-E.** A reference handed to the model resolves
  (`contentref`'s own invariant).

## 6. Open decisions for a later Step 0

This plan has **not** been challenged as a plan - it was assembled from two
challenged halves. It needs its own Step 0 before implementation, after P1.

1. Does rebuilding the ledger per `Prepare` cost anything measurable on a
   long history, and can it be incremental without becoming accumulated
   again?
2. Is `run_command` dedup safe at all? Identical output from two runs does
   not mean the world is unchanged, and telling the model "identical to
   step 14" may license a wrong inference about system state.
3. What does a substitution look like to `toolResultBodyFailed`
   (`loop_tools.go:188`), which scans `run_command` bodies for `exit=`?
   Overview §8.6 is the same hazard from the shaping side.
4. Does the derived-ledger rule survive a resumed session, where
   `l.Messages` is rehydrated from storage?

## 7. Delivery slices

0. **P1** - elision notices carry a `contentref` handle. Not this plan's
   work; this plan does not start without it.
1. Ledger derived from the retained set, with no substitution wired.
   Testable alone.
2. Substitution at `buildExecResult` for `read_file` and `run_command`.
3. Telemetry: bytes saved, substitutions emitted, referents later read.

## 8. Required tests

- Second identical `read_file` result substitutes; a one-byte-different
  result does not.
- After a compaction that elided the referent, **no** substitution is
  emitted for it (the P1 case, and the reason this plan is blocked).
- Substituted result is never larger than the body it replaces, trailer
  included.
- `grep` results are never substituted.
- A substituted body's referent is not elided by a later compaction.
- Resumed session: the derived ledger matches the rehydrated history.
- Substitution never breaks tool pairing.

## 9. Out of scope

- Superseding possibly-stale results for the same resource. That was plan
  `09` and it is stopped.
- Retroactive dedup over history. Forbidden by INV-CE-B.
- Cross-session dedup.
- `grep` result dedup at any granularity.
