# tools/04 - Recursive `list_dir`: tree view with sizes and gitignore

**Status:** DESIGN VALIDATED (2026-08-02) - baselines verified against HEAD;
decisions resolved below. Ready for ADLC Step 0 hostile challenge, then
implementation.
**Date:** 2026-08-02 (revised after code validation)
**Depends on:** nothing hard. Coordinates with `tools/03` D1
(`KindFileWritten` event - this plan's matcher invalidation uses the same
seam; whichever lands first creates it).
**Blast radius:** LOW-MEDIUM - `list_dir` schema (additive), gitignore
matcher hardening (shared by grep/glob, so regressions there are in scope).

## 1. Verified baseline

Much of the original plan's ignore-engine scope has already landed; the
remaining work is `list_dir` itself plus hardening the existing matcher.

- **Ignore engine exists**: `gitignoreMatcher`
  (`internal/tools/gitignore.go:18-28`), backed by
  `git.sr.ht/~jamesponddotco/gitignore-go`, constructed once in
  `registerSearchTools` (`default_registry.go:190-195`) and shared by
  grep/glob, always-on (walk hooks at `search.go:248-258`, `383-393`).
  Limits: **root `.gitignore` only** (nested files not loaded,
  gitignore.go:13-15), `sync.Once` load with **no invalidation or mtime
  check** (stale for process lifetime), no per-call opt-out.
- Hardcoded floor still present and coexists: `defaultIgnorePatterns =
  {".git","node_modules","vendor"}` (`default_registry.go:13`), extended by
  config `search_ignore_patterns` (`internal/config/types.go:119-121`) -
  this is the plan's "extra_ignore"; do not add a second knob.
- `glob` already has a `path` param (`search.go:419`, resolved :320-325);
  grep too. Dropped as a work item.
- `list_dir` is exactly as assumed: path-only schema (`read.go:233-237`),
  single `os.ReadDir` (:239-267), names + `/` suffix only (:286-289),
  entry cap + byte cap with notice reservation (:280-283, 291-294),
  `max_list_dir_entries` default 0 = uncapped (`defaults.go:44`). It does
  **not** use the gitignore matcher.
- No file-write event kind exists (`internal/events/event.go:16-50` has no
  `KindFileWritten`) - see tools/03 D1.

## 2. Goal

`list_dir` becomes a bounded, gitignore-aware tree so orientation costs one
call instead of a `list_dir` chain or `run_command` tree hacks; the shared
matcher stops being stale-forever.

## 3. Resolved decisions

**D1 - reuse, harden, and share the existing matcher; no new package.**
The original plan's `internal/tools/ignore` package is dead - the lib-backed
matcher stays. Hardening: (a) mtime-stat the root `.gitignore` on use and
reload on change (replaces `sync.Once` with a mutex + stamp); (b) subscribe
to `KindFileWritten` for `.gitignore` paths when tools/03 lands the event -
the stat makes the event advisory, same double-guard pattern as tools/03 D3.
Hierarchical (nested) `.gitignore` support: **deferred** - the lib is
root-only; verify whether it supports multi-file compilation before
promising it. v1 documents root-only honestly in tool descriptions.

**D2 - no `respect_gitignore` param.** grep/glob are already always-on with
no opt-out and nobody has needed one; `list_dir` matches them for
consistency. Collapsed-summary lines (3.2) make ignored directories visible,
which was the real need behind an opt-out. Revisit only on demand.

**D3 - matcher construction hoists** from `registerSearchTools` to
`registerDefaultTools` (`default_registry.go:198-207`) so `list_dir`
receives the same instance - one matcher, one semantics, one test suite.

## 4. Design

### 4.1 Additive schema

```json
{ "path": ".", "depth": 3, "include_size": true }
```

- `depth`: default 1 (today's behavior), max 16.
- `include_size`: default true when `depth > 1`, false at depth 1 (keeps
  the single-level output byte-identical to today - golden test).

### 4.2 Output (recursive mode)

Indented tree, one entry per line: `name[/]  <size>`.

- Gitignored directories appear as one collapsed line
  (`node_modules/  (ignored)`) - visible, never descended, never silently
  absent. No entry counting inside ignored dirs (that would walk them,
  defeating the point; correction to the original plan).
- Children cut by depth: `dir/ ...` marker, and a per-listing tail notice
  `... N entries beyond depth`.
- Both `max_list_dir_entries` and the byte budget apply exactly as today,
  same notice-reservation accounting (`read.go:273, 280, 303` patterns).
  Uncapped defaults stay uncapped per plan `48`'s decision.
- Sizes are file byte counts from the walk's `DirEntry.Info()`; no
  directory aggregation (forces full walks of collapsed subtrees).
- Walk order deterministic (ReadDir's lexical order), ctx checked between
  directories.

### 4.3 Matcher hardening (D1)

- `gitignoreMatcher` gains reload-on-mtime-change; `Match/MatchRel/IsDir`
  contracts unchanged, so grep/glob need zero call-site edits.
- Behavior change worth stating: grep/glob results can now change
  mid-session after a `.gitignore` edit - that is the fix, not a
  regression; changelog entry required.

## 5. Invariants

- `depth: 1` default call output byte-identical to today (golden).
- Gitignored directories summarized, never invisible, never descended.
- One matcher instance across list_dir/grep/glob (D3); semantics identical
  by construction.
- `.gitignore` edits take effect on the next tool call (mtime guard),
  event or no event.
- Walk respects ctx cancellation; entry/byte caps honored with honest
  notices in recursive mode.

## 6. Implementation steps

1. Matcher hardening (mutex + mtime stamp replacing `sync.Once`);
   concurrency test under `-race` (grep/glob share it in parallel
   batches).
2. Hoist matcher construction (D3); wire into `listDirTool`.
3. Recursive walk with depth/size/collapse per 4.2; schema + description.
4. Golden test for depth-1 byte-compatibility; recursive fixtures
   (Rust `target/`, Python `.venv/` via a fixture `.gitignore`).
5. Changelog for the mid-session reload behavior change; docs.
6. If tools/03's `KindFileWritten` exists by then, subscribe (advisory).

## 7. Testing

- Depth/entry/byte truncation matrix with notices.
- Collapsed ignored dirs at every depth; root-only semantics documented
  and tested (nested `.gitignore` fixture asserting current lib behavior,
  so a future lib upgrade that adds hierarchy is caught).
- Reload: edit `.gitignore` -> next list_dir/grep/glob call reflects it.
- Race: parallel grep+glob+list_dir during a reload.
- Regression: grep/glob outputs unchanged on a repo whose `.gitignore` is
  untouched during the session.

## 8. Failure analysis

- Pathological `.gitignore` (thousands of patterns): compiled once per
  mtime change, per-entry match is the walk cost - benchmark fixture in CI.
- Huge tree at depth 16: entry/byte caps bound the output; the walk itself
  is bounded by SkipDir on ignored/collapsed dirs and ctx cancellation.
- Stat-per-call overhead on the matcher: one stat of one path per tool
  call - negligible; measured in the benchmark fixture regardless.
