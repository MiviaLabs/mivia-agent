# 51.07 - Truncated remainders become referenceable, not lost

**Status:** DESIGN - ADLC Step 0 not run.
**Date:** 2026-08-02
**Part of:** program `51` (`00-overview.md`).
**Depends on:** `48` §3.1 (truncate instead of destroy). This plan is the
layer above it and must not be built first.
**Blast radius:** MEDIUM - tool result content, durable content storage, and
one new model-facing affordance.

## 1. Goal

When the harness shortens a tool result, the discarded remainder stays
retrievable through a content reference, so the agent can page it without
re-running the tool.

## 2. Verified baseline

- Search tools append `... truncated at %d bytes` / `... truncated at %d
  matches` and the remainder is simply not produced
  (`internal/tools/search.go:27-28`). The byte budget exists because a match
  count bounds no number of bytes.
- `grep` already supports `offset`/`limit` pagination and emits a
  `... %d more matches (use offset=%d to continue)` trailer - but that is
  pagination over a *re-executed* walk, not over a stored result.
- `read_file` refuses whole-file reads above `maxBytes` and tells the model
  to re-call with `offset`/`limit` (`internal/tools/read.go`).
- The dispatcher currently destroys over-ceiling results
  (`overCeilingError`, `internal/runtime/output_ceiling.go`). Plan `48` §3.1
  converts this to tail-truncation with a notice.
- `contentref.Reference(kind, data)` mints `ref:<kind>:<sha256>` and is the
  single canonical minter; `internal/ledger` re-exports it and already
  persists `OutputRef`/`ErrorRef` on task snapshots
  (`internal/ledger/storage_projection.go:232`).

## 3. The defect

Every truncation path today is **lossy and re-entrant**: the only recovery
is to run the tool again with different arguments. That is the worst of both
worlds - the harness paid to produce the bytes, threw them away, and the
model's cheapest route to them is to pay again. For a non-deterministic tool
(`run_command` against a live system) re-running may not even return the
same output, so the discarded remainder was not merely expensive, it was
unique.

Plan `48` §3.1 fixes the *destruction*, which is the acute bug. It does not
make the removed tail reachable.

## 4. Design

### 4.1 Spool on truncation

When a result is shortened for any harness reason, the **full** result is
written to the existing content store and its `contentref` is minted. The
notice the model sees names the reference and the shape of what was kept:

```
... truncated: kept 12000 of 918233 bytes (ref:output:<64 hex>)
```

The reference is the existing canonical format, so no second reference
grammar is introduced (`contentref` doc comment: one minter, one parser).

### 4.2 Reading a remainder

A model-facing affordance that takes a reference plus `offset`/`limit` in
**bytes or lines** and returns that window of the stored content. Whether
this is a new tool or a parameter on existing tools is an open decision
(§6.1). It must be language- and project-generic (INV-CE-D).

Reads of a stored remainder are subject to the same per-call result budget
as any other tool result, so paging cannot be used to smuggle the whole
payload back in one call.

### 4.3 Retention

Spooled content is session-scoped and bounded. `internal/ledger` already has
content-retention machinery (`content_retention_test.go`) and the storage
layer already owns lifecycle and deletion. This plan uses those; it does not
add a second retention policy.

### 4.4 Deduplication falls out for free

Two identical results mint the same digest and store one copy. This is the
same primitive `03`'s seen-ledger keys on, which is why both plans must use
`contentref` and not a private hash.

## 5. Invariants

- **INV-CE-07-A.** A reference handed to the model resolves, or it is not
  handed to the model. (This is `contentref`'s existing stated invariant;
  this plan must not be the first place it is violated.)
- **INV-CE-07-B.** Spooling never enlarges what the model sees. The notice
  is accounted against the same budget as the content it replaces, using the
  existing notice-reserve accounting.
- **INV-CE-07-C.** A truncation with no successful spool degrades to
  today's plain notice. Storage failure must not fail the tool call.
- **INV-CE-07-D.** Spooled content is subject to the same redaction and
  secret-path rules as the inline result. Storing bytes the harness would
  have refused to show is a privacy regression, not a saving.

## 6. Open decisions for Step 0

1. **New tool, or a `ref` parameter on `read_file`?** A new tool adds
   schema mass on every turn (see `05`); a parameter overloads a
   path-shaped tool with a non-path input, which its description explicitly
   warns against today. Neither is obviously right.
2. **Does spooling apply to `run_command`?** Its output can contain live
   secrets that the redaction pass handles inline. INV-CE-07-D says the
   stored copy must be redacted identically - confirm the redaction seam is
   reachable from the spool point before committing.
3. **Session scope or workspace scope?** Cross-session references would let
   a resumed session read a prior run's remainder, which is useful and is
   also a data-lifetime question the storage owners must answer.
4. **Does this conflict with `48`'s uncapped default?** Under uncapped
   config, truncation is rare, so this plan's value is concentrated in
   operator-capped deployments. Step 0 should decide whether that is
   sufficient justification.

## 7. Delivery slices

1. Spool + reference in the notice, no reader. Immediately useful for
   audit; the reference is already resolvable by host tooling.
2. The model-facing reader (after §6.1 is decided).
3. Retention wiring and telemetry: bytes spooled, references resolved,
   references never read.

## 8. Required tests

- A truncated result's spooled content, reassembled through the reader,
  equals the untruncated tool output byte for byte.
- Storage failure produces exactly today's notice and a successful call.
- The notice fits inside the existing truncation reserve.
- A secret-bearing result is redacted identically inline and in storage.
- Two identical results produce one stored object and equal references.
- An unresolvable or forged reference is refused, not silently emptied.

## 9. Out of scope

- Changing when truncation happens (that is `48` and `08`).
- Compressing stored content.
- Cross-workspace reference sharing.
