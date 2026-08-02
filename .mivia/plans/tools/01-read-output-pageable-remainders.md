# tools/01 - `read_output`: page stored remainders of truncated results

**Status:** DESIGN - thin delta over `51-harness-context-economics/07`.
**Date:** 2026-08-02
**Depends on:** `51/07` (truncated remainders stored under a content ref) and
`48` §3.1 (truncate instead of destroy). This plan is only the model-facing
tool on top; if `51/07` ships its own affordance, close this plan as merged.
**Blast radius:** LOW-MEDIUM - one new workspace tool, no storage changes
beyond what `51/07` defines.

## 1. Goal

When a tool result was truncated, the agent should page the stored remainder
instead of re-running the tool (a re-run is slower, may be non-deterministic,
and re-pays the walk). `51/07` makes remainders referenceable; this plan
specifies the tool the model uses to read them.

## 2. Design

- New workspace tool `read_output`:

  ```json
  { "ref": "ref:output:<sha256>", "offset": 0, "limit": 65536 }
  ```

  Byte-offset pagination with `next_offset` continuation, copying the
  `ledger_read` contract (`internal/cli/ledger_tools.go`) - the one tool that
  already does pagination correctly. Declares `ResultBudgetBytes`; a page is
  never itself truncated silently (limit clamps to budget, honestly stated).
- Truncation notices (from `48`/`51/07`) must carry the ref and total size so
  the model can page deliberately:
  `... truncated: kept X of Y bytes (remainder: ref:output:..., use read_output)`.
- Scope: available in `ScopeRoot` and `ScopeSpawned` - spawned agents are the
  main producers of huge results and need their own paging. Visibility is
  caller-scoped: an agent may only read refs minted for its own
  session/run (reuse the ledger's caller-scoped visibility rules).
- Refs resolve or the notice omits them (mirror INV-AG-10: never emit a ref
  whose write failed).
- Retention: pages come from the store `51/07` defines; this plan adds no
  retention policy of its own.

## 3. Invariants

- `read_output` is read-only (`ExecutionRead`), injection-inert (returns raw
  bytes as an opaque result body, budget-bounded).
- Every emitted remainder ref resolves for the principal that received it.
- Page reassembly is byte-identical to the original remainder.

## 4. Steps

1. Land after `51/07` storage seam; confirm ref kind and visibility rules.
2. Implement tool + schema (min/max on offset/limit - requires the
   validateSchema min/max enforcement fix already prompted separately).
3. Update truncation-notice format across tools.
4. Tests: page-chaining round trip, cross-principal ref denial, unresolvable
   ref error shape, budget-clamped limit.

## 5. Failure analysis

- Model ignores the ref and re-runs the tool anyway: observable via events;
  notice wording is the mitigation.
- Ref outlives its retention window: error must say expired, not corrupt.
