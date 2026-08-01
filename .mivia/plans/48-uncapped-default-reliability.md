# 48 - Uncapped-by-default reliability: make the system safe without caps

**Status:** DESIGN
**Date:** 2026-08-02
**Depends on:** nothing.
**Blocks:** makes plan `49` (compaction elision) more important, since
uncapped results make elision the primary context-cost control.
**Blast radius:** MEDIUM-HIGH - dispatcher ceiling semantics, contextstate
persistence limits, tool truncation paths, `mivia.toml` config surface.

## 1. Decision

**Defaults stay uncapped.** Capped defaults make agents unreliable: a
truncated grep or build log silently hides the answer and the agent acts on
partial data. `0` remains "unlimited" and remains the shipped default for
`max_read_bytes`, `max_output_bytes`, `max_tool_result_bytes`,
`max_list_dir_entries`, `max_write_kb`.

The problem is therefore inverted from the original draft of this plan: the
rest of the system currently *assumes* bounded results and misbehaves when
they are not. Fix those parts so the uncapped default is actually reliable,
and expose every limit as an explicit `mivia.toml` knob for operators who
want bounds.

## 2. Current breakage under uncapped defaults

1. **Dispatcher ceiling destroys results.** `internal/runtime/output_ceiling.go`
   hard-fails any result over the effective ceiling (`overCeilingError`): the
   agent pays for the tool run and gets nothing back - the exact
   unreliability the uncapped default is meant to avoid, reintroduced one
   layer up.
2. **Durable store rejects large events.** `contextstate.MaxSourceEventBytes`
   is a compile-time 64 KiB hard rejection (`contracts.go`,
   `SanitizeSourcePayload`). An uncapped tool result the loop accepted can be
   unpersistable; the last "fix" was bumping the constant (`16512b2`).
   Companion compile-time literals (`MaxCommitEventBytes`, etc.) have no
   config knobs either.
3. **run_command head-cut loses failures.** When an operator *does* set
   `max_output_bytes`, `dualCapture` keeps the head; compilers print errors
   last, so the bound discards exactly the part that matters.
4. **`search_replace` has no size guard at all** (whole-file read, no
   declared budget) and destroys file mode on write (`os.WriteFile(0644)`).
5. **Memory backstop is the only real bound**: 256 MiB per read. Acceptable
   as an OOM guard, but it must fail with a message that tells the model how
   to proceed (window reads), not a bare error.

## 3. Design

### 3.1 Ceiling: truncate, never destroy honest output

Replace destroy-on-over-ceiling with tail-truncation at a rune boundary plus
the standard `... (truncated: kept X of Y bytes)` notice (reusing
`trimPartialRune` and the notice-reserve accounting). The destructive path is
retained only as a runaway backstop at `ceiling x 4`, and only when the
operator has set explicit bounds - under pure uncapped config the backstop is
the 256 MiB memory guard.

### 3.2 Durable store: chunk instead of reject

Large source-event payloads must persist, whatever their size:

- Add payload chunking in the storage layer: a payload larger than the chunk
  size is stored as an ordered chunk sequence under one content ref;
  `ReadPayload`/`ReadRange` reassemble transparently. `MaxSourceEventBytes`
  stops being a rejection bound and becomes the chunk size.
- Existing validation paths (`SourceEvent.Validate`, `PayloadRecord`,
  `SanitizeSourcePayload`) accept any size; per-chunk invariants replace the
  whole-payload cap. Schema migration adds a `chunk_index`/`chunk_count` (or
  a child table) - design detail settled at implementation with a proper
  migration (learn from the v2 dirty-flag crash window: version bump and
  dirty-clear must be atomic).
- `MaxSessionStateBytes` (64 MiB) and `MaxExportBytes` stay as genuine
  storage-integrity bounds but become configurable (3.4).

### 3.3 Bounded-mode fixes (for operators who opt into caps)

- `dualCapture` keeps head 1/3 + tail 2/3 with an elision marker; `exit=`
  header always preserved.
- `search_replace`: declared result budget, file-size guard tied to the
  effective read bound (or the 256 MiB backstop when uncapped), and
  mode-preserving writes (stat, then write with original perm) - the mode
  bug applies regardless of caps.
- Oversize refusals (`read_file` full-file path, memory backstop) must state
  the file size and instruct windowed re-reads (`offset`/`limit`).

### 3.4 `mivia.toml` config surface

All limits become explicit, documented knobs under `[tools]` and a new
`[context.limits]` section; `0` = unlimited everywhere; shipped defaults all
`0` except genuine integrity bounds:

```toml
[tools]
max_read_bytes        = 0   # unlimited (default)
max_tool_result_bytes = 0
max_output_bytes      = 0
max_list_dir_entries  = 0
max_write_kb          = 0
memory_backstop_mb    = 256 # OOM guard, not a context cap

[context.limits]
source_event_chunk_kb = 64  # chunking granularity, replaces the hard cap
session_state_max_mb  = 64
export_max_mb         = 8
```

Config-load validation: warn (not clamp) when a nonzero
`max_tool_result_bytes` exceeds what a single provider request can usefully
carry; never silently rewrite operator values.

## 4. Invariants

- No tool result the loop accepted is ever destroyed downstream: the
  dispatcher truncates with an honest notice (bounded mode) or passes through
  (uncapped mode); the durable store persists it via chunking.
- Truncation, where configured, never splits a UTF-8 rune or tool pairing,
  and notices are paid out of the budget (existing reserve accounting).
- `0` means unlimited, everywhere, and is the default - no behavior change
  for existing uncapped installs except *more* reliability (no ceiling
  destruction, no persistence rejection).
- Chunked payloads are byte-identical on reassembly (round-trip tested) and
  content refs remain SHA-256 of the full payload.
- File mode preserved by `search_replace`/`write_file` rewrites.

## 5. Implementation steps

1. Ceiling truncate-instead-of-destroy in `internal/runtime`; keep destroy
   only at the x4 runaway backstop in bounded mode; update
   `result_cap_registry_test.go` gates.
2. Payload chunking: schema migration (atomic version/dirty handling),
   storage write/read paths, `SanitizeSourcePayload` per-chunk validation.
3. Convert `MaxSourceEventBytes` and friends from compile-time literals to
   config-backed values (`[context.limits]`), defaults preserving current
   numbers as chunk/integrity bounds.
4. `dualCapture` head+tail retention; refusal messages with windowing
   guidance.
5. `search_replace` size guard, declared budget, mode preservation.
6. `mivia.toml` docs + example config; startup log line summarizing effective
   limits (all-unlimited is stated explicitly once).

## 6. Testing

- Uncapped end-to-end: multi-MB grep/read/run_command result flows loop ->
  dispatcher -> durable commit with zero truncation and zero rejection;
  reassembled payload byte-identical.
- Bounded matrix: each tool at cap-1 / cap / cap+1 / runaway asserting
  pass / truncate-with-notice / destroy-only-at-backstop.
- Migration crash test: kill between version bump and dirty clear must not
  brick the store (regression for the v2 pattern).
- run_command failing-build fixture: error tail survives under a bound.
- `search_replace` on an executable preserves `+x`.

## 7. Failure analysis

- Unbounded results still cost context: that is a *context* problem, owned by
  plan `49` (elision stubs large bodies at compaction time, with the durable
  chunked payload as the recall source) - reliability first, cost second.
- Chunking bug corrupts payloads: content-ref SHA verification on reassembly
  fails closed to an explicit error, never silent truncation.
- Operator sets tiny bounds and agents degrade: their explicit choice;
  truncation notices with totals keep it observable.
