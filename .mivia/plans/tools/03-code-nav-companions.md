# tools/03 - Code-nav companions: `list_symbols`, `go_to_definition`, cached loads

**Status:** DESIGN VALIDATED (2026-08-02) - baselines verified against HEAD
(`304f42d` era); decisions resolved below. Ready for ADLC Step 0 hostile
challenge, then implementation.
**Date:** 2026-08-02 (revised after code validation)
**Depends on:** nothing. Complements `find_references`
(`internal/tools/find_references.go`, `internal/codeintel`).
**Blast radius:** MEDIUM - two new tools, a codeintel cache, and a **new
file-write invalidation seam in `internal/tools/write.go`** (none exists
today); no changes to existing tool contracts.

## 1. Problem (verified)

- `find_references` is a lone island: no outline, no definition jump, so the
  model reads whole files to orient.
- Every query runs a full `packages.Load(cfg, "./...")`
  (`internal/codeintel/analyzer.go:85`, mode incl. NeedTypes/NeedTypesInfo,
  `Tests: true`); `Analyzer` holds only `root string`
  (`analyzer.go:17-19`) - **no cache, no mutex**.
- Go-only: `analyzer.go:47-50` stats `go.mod`, else `ErrUnavailable`. The
  error IS surfaced explicitly in the tool's JSON result
  (`find_references.go:114-120`) - not silent; the cost is that the model
  falls back to grep-and-read by its own choice.
- Corrections from validation: the "default 50" doc-vs-code drift is
  **already fixed** (`default_registry.go:302` sets `limit: 50`; analyzer
  clamps 0→50 at `analyzer.go:50-52`) - no work item. Gitignore is already
  wired into grep/glob via `git.sr.ht/~jamesponddotco/gitignore-go`
  (`internal/tools/gitignore.go`, `default_registry.go:183-195`) - reuse
  `newGitignoreMatcher` where relevant, do not build ignore logic here.

## 2. Goal

Two cheap companions plus a shared cached analyzer, so "what is in this
file / where is this defined" costs one small call instead of a whole-file
read - and `find_references` gets the latency win for free.

## 3. Resolved decisions

**D1 - invalidation seam must be created, not reused.** The write path has
no hook: all three writers funnel through `rewriteRegularFileContents`
(`internal/tools/write.go:154`; callers `write.go:122` write_file,
`edit.go:128` search_replace, `write.go:268` multi_edit), and the async
events bus (`internal/events/bus.go`, per-subscriber bounded queues since
`bb001aa`/`869d843`) has **no file-mutation Kind**. Decision: publish a new
`KindFileWritten` event (workspace-relative path only, no content) from each
writer's `Execute` after a successful write, and have the codeintel cache
subscribe. Rationale over a package-level callback: the bus already handles
async delivery, panic containment, and multiple future consumers (tools/04
gitignore-cache, plan 51 members) will want the same signal. The event is
advisory; the cache also mtime-checks on read (D3), so a dropped event
(bounded-queue overflow) degrades to a stat, never to staleness.

**D2 - `list_symbols` file mode uses `go/parser` only** (single file, no
type check, no `packages.Load`) - zero hits for `go/parser` in the repo
today, this is new but stdlib-only. Workspace symbol search uses the cached
analyzer.

**D3 - cache shape: single snapshot, stat-verified.** One cached
`[]*packages.Package` per Analyzer, guarded by a mutex (Analyzer gains
state; today it is shared-nothing - audit constructors at
`NewAnalyzer(dir)`, `analyzer.go:22`, and the registry wiring at
`default_registry.go:293-303` for reuse across calls: if the registry
constructs per-tool instances, the cache must live in a shared object all
three nav tools receive). Invalidation: drop the snapshot on
`KindFileWritten` under the workspace for `.go`/`go.mod`/`go.sum` paths;
plus on every cache hit, stat go.mod/go.sum and the event-dirty set - a
mismatch drops the snapshot. No LRU, no partial invalidation in v1.

**D4 - cut the approximate non-Go fallback.** Validation confirmed no
tree-sitter/LSP dep exists and the Go path alone justifies the plan; a
regex outline for other languages is unproven value with real
wrong-answer risk. Non-Go requests get the same explicit
`unsupported language` / `analysis unavailable` JSON error shape
`find_references` already uses. Polyglot support is a future plan.

## 4. Design

### 4.1 `list_symbols`

```json
{ "path": "internal/agent/loop.go" }        // file outline (parser-only)
{ "symbol_prefix": "Plan", "limit": 50 }    // workspace search (either/or)
```

Per symbol: name, kind (func/method/type/const/var/field), receiver, line
span, exported flag, one-line signature. File mode ordered by position - it
IS the outline. Output copies `find_references`' proven self-truncation:
binary search over the prefix to stay in budget, `truncated: true`, minimal
fallback (`marshalBudgeted` pattern, `find_references.go:145-193`).
Declares both `Capability.MaxResultBytes` and `ResultBudgetBytes` like
`find_references` (`:66-72`), budget 100_000 clamped by
`MaxToolResultBytes`.

### 4.2 `go_to_definition`

```json
{ "symbol": "contextmgr.Plan" }   // or { "path": "...", "line": 120, "col": 8 }
```

Returns definition site (path, line span) plus definition source text
bounded to N lines (default 40). Reuses codeintel's existing `definition`
role classification (`codeintel.go:15-19`, `roles.go`) via the cached
analyzer; position-based lookup resolves through the snapshot's TypesInfo.

### 4.3 Registration

Same advertised-iff-can-succeed rule already applied at
`default_registry.go` (`run_command` :212, `extract` :281,
`find_references` :293): file-mode-capable `list_symbols` registers with
any workspace; `go_to_definition` and workspace-mode symbol search are
Go-gated at execution (workspace may gain/lose go.mod mid-session; the
explicit JSON error covers the gap, matching `find_references`' behavior).

## 5. Invariants

- All three nav tools `ExecutionRead`, side-effect free.
- JSON outputs valid after self-truncation (binary-search pattern).
- Cache never returns positions from pre-write content: `KindFileWritten`
  + stat-on-hit double guard (D3); a write through any of the three
  writers invalidates before the next nav query completes against stale
  data (test: write → immediate query reflects new positions).
- `KindFileWritten` carries path only - no file content on the bus.
- Documented defaults match code (regression test; the drift class that
  was already fixed once must not return).

## 6. Implementation steps

1. `KindFileWritten` event kind + publish from the three writer Execute
   paths (D1); test bus delivery + overflow-degrades-to-stat.
2. Cache in codeintel per D3 (shared instance audit first); wire
   `find_references` onto it.
3. `list_symbols` file mode (`go/parser`, stdlib only).
4. `list_symbols` workspace mode + `go_to_definition` on the cached
   analyzer.
5. Registration + docs.

## 7. Testing

- Outline goldens over a fixture package (kinds, spans, receivers,
  ordering, unexported filtering).
- Definition across packages, methods, embedded fields; position-based
  lookup at boundary columns.
- Cache: write via each of write_file/search_replace/multi_edit → next
  query reflects new positions; out-of-band edit (bypassing tools) caught
  by stat-on-hit; event-queue overflow simulated.
- Self-truncation validity at tiny budgets (reuse find_references test
  approach).
- Concurrency: parallel nav queries + a write under `-race`.
- Token-economics smoke: orientation task via outline calls only,
  asserting no whole-file `read_file` in the trace.

## 8. Failure analysis

- Load cost on first query unchanged (cold cache) - acceptable; the win
  is every subsequent query. Emit elapsed in the result for observability.
- Huge generated packages blow outline budgets: self-truncation +
  `symbol_prefix` narrowing.
- Memory: one full NeedTypes snapshot held between queries; dropped on any
  relevant write. If RSS proves problematic on monorepos, add a TTL -
  measured decision, not v1.
