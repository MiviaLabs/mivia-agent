# tools/03 - Code-nav companions: `list_symbols`, `go_to_definition`, cached loads

**Status:** DESIGN LOCKED - ADLC Step 0 complete (2026-08-02). Hostile
challenge returned REWORK; all amendments applied below. Ready for
implementation.
**Date:** 2026-08-02 (revised after Step 0 challenge)
**Depends on:** nothing. Complements `find_references`
(`internal/tools/find_references.go`, `internal/codeintel`).
**Blast radius:** MEDIUM - two new tools and a codeintel cache. **No events
work, no new cross-layer dependency** (the challenge killed the
`KindFileWritten` design - see disposition).

## 0. Step 0 disposition (hostile challenge)

### Original design summary
Cache invalidated by a new `KindFileWritten` bus event published from the
three file-writer tools, double-guarded by stat-on-hit of go.mod/go.sum plus
an event-dirty set; position-based `go_to_definition`; shared-instance audit
deferred to implementation.

### Challenge findings (verdict: REWORK)
- **B1**: stat-on-hit as specified never catches out-of-band edits (editor,
  `git checkout`, gofmt) - nothing puts them in the dirty set, so "degrades
  to a stat" was false; the miss is permanent staleness.
- **B2**: `run_command` (gofmt, sed, go generate, git) rewrites `.go` files
  with no event - the three writers are not the write surface.
- **B3**: the bus is async (`bus.go:43-58`, per-subscriber queue +
  goroutine); the write-then-query invariant cannot be carried by a
  fire-and-forget channel - the next tool call can race ahead of delivery.
- **M1**: `KindFileWritten` was layer abuse (bus subscribers are all
  UI/observability; `internal/tools` has zero `internal/events` refs; bus is
  nil in some registry paths) and, once stat carries correctness, pure
  speculative generality. **Dropped from this plan entirely.**
- **M3**: event-dirty set was self-contradictory (drop-on-event vs
  stat-the-set). Deleted with the event.
- **M2**: shared-instance "audit" deferred a knowable answer - decided
  below (one analyzer, registry-owned).
- **m2**: position-based `go_to_definition` had undefined column semantics
  (byte vs UTF-16) and was the one feature forcing `NeedSyntax` retention -
  **cut from v1**.
- **m1**: writer call-site labels were transposed (`edit.go:128` is
  multi_edit; `write.go:268` is search_replace) - moot now that no writer
  instrumentation exists, kept here for the record.
- **M4**: cached `NeedSyntax|NeedTypes|NeedTypesInfo, Tests:true` snapshot
  is plausibly hundreds of MB on this repo, GBs on monorepos - measurement
  becomes a v1 step with a threshold, and `NeedSyntax` is the named
  drop-candidate.

### Locked resolution
Invalidation = **stat every snapshot file on cache hit**. Synchronous,
complete over all write paths (three writer tools, run_command,
out-of-band), no bus involvement. The plan got smaller.

## 1. Verified baseline (from validation + challenge re-verification)

- Analyzer is stateless; every query runs a full
  `packages.Load(cfg, "./...")` with NeedTypes/NeedTypesInfo, `Tests: true`
  (`internal/codeintel/analyzer.go:85`; struct `:17-19`, no cache/mutex).
- Go-only (`analyzer.go:47-50` stats go.mod -> `ErrUnavailable`), surfaced
  as explicit JSON in tool output (`find_references.go:113-120`).
- `find_references` limit-50 doc/code agreement confirmed
  (`default_registry.go:302`, `analyzer.go:51-53`) - no work item.
- `registerFindReferencesTool` constructs the Analyzer function-locally
  (`default_registry.go:298`) - no shared object exists today.
- Self-truncating JSON via binary search (`find_references.go:145-193`) is
  O(log n) marshals - cost non-issue, pattern reused as-is.

## 2. Goal

"What is in this file / where is this defined" costs one small call instead
of a whole-file read; `find_references` gets the cache latency win free.

## 3. Locked decisions

**D1 - invalidation is stat-on-hit over the full snapshot file set.** The
cached snapshot's `token.FileSet` enumerates every file that contributed;
on each cache hit, stat all of them (plus go.mod/go.sum) and compare
mtime+size against snapshot build time; any mismatch (or stat error) drops
the snapshot and reloads. ~2k stats on a warm dentry cache is single-digit
milliseconds - orders cheaper than the `packages.Load` it avoids. No
events, no dirty set, no writer instrumentation; covers editors, git,
gofmt, run_command, everything.

**D2 - `list_symbols` file mode uses `go/parser` only** (single file, no
type check, no `packages.Load`) - stdlib-only, cheap, works even when the
workspace snapshot is cold.

**D3 - one cached Analyzer, owned by the registry.** Constructed once in
`registerDefaultTools`, handed to all three nav tools (find_references
included). Mutex-guarded single snapshot; lifetime = registry lifetime; a
new registry (workspace/agent change) means a cold cache; no teardown
needed (nothing to unsubscribe - consequence of dropping the event).

**D4 - no non-Go fallback, no position mode.** Non-Go requests get the
explicit `analysis unavailable` JSON shape `find_references` already uses.
`go_to_definition` is **symbol-based only** (`{"symbol": "pkg.Ident"}`);
position mode is cut (undefined column semantics, forces AST retention,
and symbol mode + `list_symbols` covers the agent workflow).

**D5 - RSS is a gated v1 measurement.** Step 2 measures resident snapshot
size on this repo; threshold 512 MiB. Over threshold: drop `NeedSyntax`
from the cached mode set (definition source text is re-read from disk at
the reported position instead of from retained ASTs) and re-measure.

## 4. Design

### 4.1 `list_symbols`

```json
{ "path": "internal/agent/loop.go" }        // file outline (parser-only)
{ "symbol_prefix": "Plan", "limit": 50 }    // workspace search (either/or)
```

Per symbol: name, kind (func/method/type/const/var/field), receiver, line
span, exported flag, one-line signature. File mode ordered by position.
Output copies `find_references`' self-truncation (`marshalBudgeted`
pattern); declares both `Capability.MaxResultBytes` and
`ResultBudgetBytes`, budget 100_000 clamped by `MaxToolResultBytes`.

### 4.2 `go_to_definition`

```json
{ "symbol": "contextmgr.Plan" }
```

Returns definition site (path, line span) plus definition source text
bounded to 40 lines, read from disk at the reported span (keeps D5's
`NeedSyntax`-drop option open). Reuses codeintel's `definition` role
classification via the cached analyzer.

### 4.3 Registration

Advertised-iff-can-succeed, matching existing conditions
(`default_registry.go`: run_command :212, extract :281, find_references
:293): file-mode-capable `list_symbols` registers with any workspace;
`go_to_definition` and workspace-mode symbol search follow
`find_references`' condition and error shape when go.mod is absent.

## 5. Invariants

- All three nav tools `ExecutionRead`, side-effect free.
- JSON outputs valid after self-truncation.
- **No stale positions, ever**: any change to any file in the snapshot -
  by any writer, tool or not - is caught by the next query's stat pass
  (D1). Test matrix: write_file / search_replace / multi_edit /
  run_command(gofmt) / direct os write, each followed immediately by a
  query asserting fresh positions.
- Stat failure = snapshot drop (fail toward reload, never toward stale).
- One analyzer instance across the three tools (D3).
- Documented defaults match code (regression class: the fixed limit-50
  drift must not return).

## 6. Implementation steps

1. Analyzer cache in `internal/codeintel`: snapshot struct (packages +
   FileSet + per-file {mtime,size} + build stamp), mutex, stat-on-hit
   validation per D1.
2. RSS measurement on this repo (D5 gate); apply `NeedSyntax` drop if over
   threshold.
3. Construct shared analyzer in `registerDefaultTools`; rewire
   `find_references` onto it (D3).
4. `list_symbols` file mode (`go/parser`).
5. `list_symbols` workspace mode + `go_to_definition` (symbol-only).
6. Registration + docs.

## 7. Testing

- Staleness matrix per Section 5 (five write paths x immediate query).
- Outline goldens (kinds, spans, receivers, ordering, unexported filter).
- Definition across packages, methods, embedded fields.
- Self-truncation validity at tiny budgets.
- Concurrency: parallel nav queries + concurrent snapshot drop under
  `-race`.
- Cold-cache latency and warm-hit stat-pass latency recorded (evidence for
  D1's cost claim).
- Token-economics smoke: orientation via outline calls only, no
  whole-file `read_file` in trace.

## 8. Failure analysis

- First query per session pays the cold load - unchanged from today;
  elapsed reported in the result.
- Snapshot thrash under heavy write activity (agent editing loop): every
  edit drops the cache, queries between edits reload. Acceptable - that is
  today's per-query behavior; the cache only ever removes cost.
- mtime granularity: same-second rewrite with identical size could
  theoretically slip past mtime+size. Mitigation: build stamp uses
  nanosecond mtimes where the FS provides them; documented residual risk,
  revisit with content hashing only if observed.
- Huge generated packages: self-truncation + `symbol_prefix` narrowing.
