# tools/04 - Recursive `list_dir`: tree view with sizes and gitignore

**Status:** DESIGN LOCKED - ADLC Step 0 complete (2026-08-02). Challenge
verdict: REWORK via amendments ("none reopen the decisions"); all applied
below. Ready for implementation.
**Date:** 2026-08-02 (revised after Step 0 challenge)
**Depends on:** nothing hard.
**Blast radius:** LOW-MEDIUM - `list_dir` schema (additive), shared ignore
decision refactor (grep/glob call sites change - snapshot semantics, see
D1/D3), matcher hardening.

## 0. Step 0 disposition (hostile challenge)

Direction survived (reuse-and-harden, always-on, shared construction).
Findings applied:

- **S1**: mutex+mtime reload in a matcher consulted per walk step produces
  a torn ignore view mid-walk; "zero call-site edits" was false. -> D1 now
  specifies **snapshot semantics**: walks capture an immutable compiled
  rule set at entry.
- **S2**: hoisting only the matcher left the hardcoded floor
  (`defaultIgnorePatterns` + `search_ignore_patterns`, composed inside
  `registerSearchTools`, `default_registry.go:185-187`) behind - on a
  `.gitignore`-less repo `node_modules` would descend. -> D3 hoists the
  whole **ignore decision**, not the matcher.
- **C2 (BLOCKER)**: the output contract conflated the two ignore
  mechanisms; the plan's own `node_modules/ (ignored)` example was
  unimplementable as spec'd. -> unified `ignored` definition in 4.2.
- **S3**: notice-reservation arithmetic does not transfer to recursion
  (omitted counts unknowable up front). -> fixed worst-case reserve +
  co-occurrence rules in 4.2.
- **C1**: conditional `include_size` default is not expressible in
  `validateSchema` (no default machinery, `tools.go:193-218`). ->
  `*bool` decode + description prose.
- **C4** entry cap redefined as total; **C5** staleness stamp upgraded;
  **C6** secret paths in descent specified; **C3** `Info()` races
  specified; **C7** symlinks pinned to lstat semantics; **S4** explicit
  path into an ignored dir always lists.

## 1. Verified baseline

- `gitignoreMatcher` (`internal/tools/gitignore.go:18-28`), lib-backed
  (`gitignore-go`), root-`.gitignore`-only, `sync.Once` load, no
  invalidation, always-on in grep/glob (`search.go:248-258, 383-393`).
- Skip decision is two mechanisms: `ignoreDir(name, ignorePatterns)`
  (hardcoded floor + `search_ignore_patterns`) OR `gi.IsDir(rel)`.
- `list_dir`: path-only schema, single `os.ReadDir`, names + `/` only,
  entry/byte caps with up-front notice reservation (`read.go:233-294`),
  no matcher, `max_list_dir_entries` default 0 = uncapped.
- `glob` already has `path`; secret paths skipped mid-walk by grep/glob
  (`search.go:262, 394`) but only argument-checked by list_dir
  (`read.go:256`).

## 2. Goal

`list_dir` becomes a bounded, ignore-aware tree; the shared ignore decision
becomes consistent, snapshot-stable, and reload-capable across all three
tools.

## 3. Locked decisions

**D1 - snapshot semantics for the ignore decision.** The matcher gains
`snapshot() ignoreView`: under a mutex, stat the root `.gitignore` and
reload if the stamp changed, then return an immutable value combining the
compiled gitignore rules AND the pattern list (see D3). Every walk (grep,
glob, recursive list_dir) captures one snapshot at entry and uses it for
the whole walk - one call, one rule set. This IS a call-site edit in
grep/glob (each walk callback closes over the snapshot instead of the
matcher); budgeted as such. Staleness stamp = `(mtime, size, content
hash)` - the file is tiny and already being read; hashing kills the
same-second-edit hole (C5) outright.

**D2 - always-on, no opt-out param, with the descent rule.** An explicitly
requested `path` is always listed, even inside an ignored directory;
ignore rules govern only descent below the requested root (S4). Collapsed
lines keep ignored dirs visible.

**D3 - hoist the ignore decision, not the matcher.** One helper owned at
registry level: `ignoreView.ShouldIgnoreDir(name, rel) bool` /
`ShouldIgnoreFile(name, rel) bool`, composing the hardcoded floor,
`search_ignore_patterns`, and gitignore. Constructed once in
`registerDefaultTools`, shared by grep/glob/list_dir. `.git` remains
always ignored.

## 4. Design

### 4.1 Additive schema

```json
{ "path": ".", "depth": 3, "include_size": true }
```

- `depth`: integer, default 1 (today's behavior), max 16.
- `include_size`: decoded as `*bool`; nil -> `depth > 1`. The conditional
  default lives in Execute and is stated in the tool description prose -
  `validateSchema` has no default machinery (C1). Explicit
  `{"depth":1,"include_size":true}` legitimately changes output; the
  byte-identity invariant applies to the unset case only.

### 4.2 Output (recursive mode)

Indented tree, one entry per line: `name[/]  <size>`.

- **Ignored** is one predicate: `ShouldIgnoreDir` per D3 - built-in floor,
  configured patterns, and gitignore all render identically as
  `name/  (ignored)`, one collapsed line, never descended, never silently
  absent (C2). A `.gitignore`-less repo still collapses `node_modules/`.
- Secret-matching entries (C6): directories matching `isSecretPath`
  render `name/  (blocked)` and are never descended; secret-matching
  files are listed name-only, no size. Matches the grep/glob mid-walk
  precedent.
- Symlinks are never followed (C7): lstat-based `DirEntry` semantics,
  listed as plain entries; loop-safe by construction.
- `DirEntry.Info()` errors (C3): `ErrNotExist` -> entry skipped; any other
  error -> name emitted without size and counted in a trailing
  `... N entries unreadable` notice (grep's `walkErrors` pattern).
- Depth cut: `dir/ ...` marker per cut directory plus one tail notice
  `... N entries beyond depth`, where N counts entries **encountered but
  not emitted** - never a claim about unwalked subtrees (S3).
- **Entry cap (C4)**: in recursive mode `max_list_dir_entries` bounds
  total emitted lines across the walk; the walk stops at the cap with
  `... truncated (N more encountered)`.
- **Reservation (S3)**: recursive mode reserves a fixed worst-case notice
  block up front - all three notice species (byte cap, entry cap,
  beyond-depth) formatted with max-width (20-digit) counts. Notice order
  when co-occurring: unreadable, beyond-depth, entry cap, byte cap, each
  on its own line, at most once each. Content+notices <= maxBytes always.
- Sizes are file byte counts; no directory aggregation. Walk order is
  ReadDir's lexical order; ctx checked between directories. Uncapped
  defaults stay uncapped per plan `48`.

## 5. Invariants

- `depth` unset/1 with `include_size` unset: output byte-identical to
  today (golden).
- One ignore predicate across list_dir/grep/glob (D3); every walk sees
  exactly one rule-set snapshot (D1) - no torn views.
- Ignored (any mechanism) and secret-blocked dirs are summarized, never
  invisible, never descended; explicitly requested paths always list (D2).
- `.gitignore` edits take effect on the next tool call, including
  same-second same-size edits (content-hash stamp).
- Symlinks never followed. Entry/byte caps honored with the 4.2 notice
  contract; notices never exceed the reserved block.

## 6. Implementation steps

1. `ignoreView` snapshot type + matcher hardening (mutex, (mtime, size,
   hash) stamp); `-race` test with concurrent walks during reload.
2. Hoist composition to `registerDefaultTools` (D3); convert grep/glob
   walks to snapshot capture (D1) - regression: outputs unchanged when
   `.gitignore` is untouched.
3. Recursive walk in list_dir per 4.2 (ignore, secret, symlink, Info-error,
   caps, reservation).
4. Goldens: depth-1 unset-params byte-identity; recursive fixtures - Rust
   `target/`+`.venv/` via fixture `.gitignore` AND a no-`.gitignore`
   fixture asserting `node_modules/ (ignored)` (C2 test).
5. Changelog: mid-session `.gitignore` reload is a behavior change for
   grep/glob; docs.

## 7. Testing

- Depth/entry/byte truncation matrix incl. co-occurring notices and the
  worst-case reserve bound.
- Ignore matrix: built-in floor only / config patterns / gitignore /
  negation / all three at once - identical `(ignored)` rendering.
- Secret dir + secret file rendering in descent; explicit path into an
  ignored dir lists (D2).
- Reload: edit `.gitignore` -> next call reflects it; same-second
  same-size edit caught (hash); torn-view race test (walk started before
  edit finishes under the entry snapshot).
- Symlink loop fixture; deleted-mid-walk file fixture.
- Benchmark fixture: pathological `.gitignore` (1000s of patterns)
  per-entry match cost.

## 8. Failure analysis

- Reload thrash if `.gitignore` is rewritten every turn: cost is one
  small-file read+hash per tool call and one recompile per actual change -
  bounded, measured in the benchmark.
- Cap interactions producing empty-looking output on huge trees: notices
  always state encountered counts, so truncation is never mistaken for
  emptiness.
- Two tools racing a reload each get *a* consistent snapshot; outputs may
  differ from each other but each is internally coherent - documented,
  tested, acceptable.
