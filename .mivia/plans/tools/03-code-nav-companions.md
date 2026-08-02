# tools/03 - Code-nav companions: `list_symbols`, `go_to_definition`, cached loads

**Status:** DESIGN
**Date:** 2026-08-02
**Depends on:** nothing. Complements `find_references`
(`internal/tools/find_references.go`, `internal/codeintel`).
**Blast radius:** MEDIUM - new tools, codeintel caching layer; no changes to
existing tool contracts.

## 1. Problem

`find_references` is a lone island: no outline, no definition jump, so the
model reads whole files to orient - the single most common token waste in
code navigation. Each query re-runs a full `packages.Load` (expensive,
uncached), and the backend is Go-only (`ErrUnavailable` without `go.mod`),
failing silently into grep-and-read behavior.

## 2. Goal

Two cheap companions plus a shared cached analyzer, so "what is in this
file / where is this defined" costs one small call instead of a whole-file
read.

## 3. Design

### 3.1 `list_symbols`

```json
{ "path": "internal/agent/loop.go" }        // file outline
{ "symbol_prefix": "Plan", "limit": 50 }    // workspace symbol search (either/or)
```

Returns per symbol: name, kind (func/method/type/const/var/field), receiver,
line span, exported flag, and a one-line signature. File mode is ordered by
position - it IS the outline. Output is self-truncating valid JSON with
`truncated: true`, copying `find_references`' pattern; declares both
`Capability.MaxResultBytes` and `ResultBudgetBytes` like it does.

File-mode outline for Go needs only `go/parser` on one file (no type check)
- it must NOT pay a `packages.Load`. Workspace symbol search uses the cached
analyzer (3.3).

### 3.2 `go_to_definition`

```json
{ "symbol": "contextmgr.Plan" }   // or { "path": "...", "line": 120, "col": 8 }
```

Returns definition site (path, line span) plus the definition's source text
bounded to N lines - enough to answer "what is this" without a follow-up
read. Reuses codeintel's existing `definition` role classification.

### 3.3 Cached analyzer

- Wrap `packages.Load` behind a per-workspace cache keyed by
  (module hash of go.mod/go.sum, dirty-file set). Invalidate on
  `write_file`/`search_replace` to affected packages (hook the workspace
  write path, coarse per-package invalidation is fine).
- Bound memory: single cached snapshot, dropped on invalidation - no LRU
  complexity in v1.
- Both new tools and `find_references` share it; `find_references` gets the
  latency win for free.

### 3.4 Beyond Go (bounded scope)

Full polyglot type-checking is out of scope. v1 ships a degraded
tree-sitter-free fallback for `list_symbols` file mode only: regex/indent
outline for a small language allowlist (ts/js, python, rust) clearly marked
`"fidelity": "approximate"`. `go_to_definition` and workspace search remain
Go-only and must say so in the error (`unsupported language: <ext>`), never
silently degrade. If the fallback's value is unproven, cut it - the Go path
alone justifies the plan.

### 3.5 Registration consistency

Same rule as the conditional-registration fix (already prompted): a tool is
advertised iff it can succeed. `go_to_definition` and workspace-mode
`list_symbols` register only with a Go workspace; file-mode `list_symbols`
registers whenever a workspace exists.

## 4. Invariants

- All three tools `ExecutionRead`, side-effect free.
- JSON outputs always valid after self-truncation.
- Cache staleness never returns positions from pre-edit file content
  (invalidation-on-write test), and documented default limits match code
  (regression for the `find_references` "default 50" drift).

## 5. Steps

1. Cached analyzer in `internal/codeintel` + invalidation hook; wire
   `find_references` onto it.
2. `list_symbols` file mode (parser-only) + workspace mode.
3. `go_to_definition`.
4. Approximate fallback (or cut, per 3.4 decision at review).
5. Registration rules + docs.

## 6. Testing

- Outline goldens over a fixture package (kinds, spans, ordering).
- Definition across packages, methods with receivers, embedded fields.
- Cache: edit file -> immediate re-query reflects new positions.
- Self-truncation validity at tiny budgets.
- Token-economics smoke: orientation task solved with outline calls only,
  asserting no whole-file `read_file` in the trace.

## 7. Failure analysis

- Cache invalidation misses an out-of-band edit (user edits outside the
  agent): mtime-check the dirty set on read, cheap stat per query.
- Big generated packages blow outline budgets: self-truncation covers it;
  `symbol_prefix` narrows.
