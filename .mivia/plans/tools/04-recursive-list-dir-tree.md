# tools/04 - Recursive `list_dir`: tree view with sizes and gitignore

**Status:** DESIGN
**Date:** 2026-08-02
**Depends on:** nothing.
**Blast radius:** LOW-MEDIUM - one existing tool's schema (additive), a
shared ignore engine that `grep`/`glob` should adopt in the same change.

## 1. Problem

`list_dir` (`internal/tools/read.go`) is single-level, names-only (no size,
mtime, or type beyond a trailing `/`), forcing agents into repeated
`list_dir` chains or `run_command` tree hacks for orientation. Separately,
`grep`/`glob` hardcode `.git`/`node_modules`/`vendor` with no `.gitignore`
awareness, flooding results on Rust/Python/Java trees. Both problems share
one missing piece: an ignore engine.

## 2. Design

### 2.1 Additive schema

```json
{
  "path": ".",
  "depth": 3,            // default 1 (today's behavior), max 16
  "include_size": true,  // default true in recursive mode
  "respect_gitignore": true   // default true
}
```

`depth: 1` with defaults is byte-compatible with today's output - existing
behavior unchanged unless new params are used.

### 2.2 Output

Indented tree, one entry per line: `name[/]  <size>  [ignored-summary]`.
Directories that are gitignored appear as a single collapsed line
(`node_modules/  (ignored, 1.2k entries)`) rather than being silently
absent - the agent should know the directory exists without paying for its
contents. Per-directory entry counts when children are cut by depth
(`src/ ... 14 more entries at depth`). Both `max_list_dir_entries` and the
byte budget apply exactly as today, with the existing honest-notice reserve
accounting.

Sizes are file byte counts; no directory-size aggregation in v1 (it forces a
full walk of collapsed subtrees, defeating the point).

### 2.3 Shared ignore engine

New `internal/tools/ignore` package:

- Parses `.gitignore` files hierarchically (root + per-directory), standard
  gitignore semantics including negation; plus the existing hardcoded floor
  (`.git` always).
- Config knob `[tools] extra_ignore = []` for operator additions.
- `grep` and `glob` adopt it in this plan (flag `respect_gitignore`,
  default true) - replacing their hardcoded lists. This is the larger token
  win of the plan.
- Compiled per workspace, cached, invalidated when a `.gitignore` is
  written through the workspace write path (plus mtime stat on use).

## 3. Invariants

- Default single-level call output is unchanged (golden test).
- Gitignored directories are summarized, never invisible.
- Ignore semantics identical across `list_dir`/`grep`/`glob` (one engine,
  one test suite).
- Walk respects ctx cancellation between directories.

## 4. Steps

1. `internal/tools/ignore` engine + gitignore-semantics test corpus.
2. Recursive walk in `list_dir` with depth/entry/byte bounds and collapsed
   ignored dirs.
3. Adopt engine in `grep`/`glob`; delete hardcoded skip lists; also give
   `glob` the `path` root param it is missing.
4. Docs + tool description updates.

## 5. Testing

- Gitignore corpus: negation, nested files, directory patterns, trailing
  slashes.
- Depth/entry/byte truncation matrix with honest notices.
- Rust/Python fixture trees: `target/`, `.venv/` collapsed everywhere.
- Regression: `grep` results identical on a repo with no `.gitignore`.

## 6. Failure analysis

- Pathological `.gitignore` (thousands of patterns): compile once, cache;
  per-entry match is the walk's cost ceiling - benchmark in CI fixture.
- `respect_gitignore: true` hides the file the agent needs: it can rerun
  with `false`; collapsed-summary lines make the existence visible.
