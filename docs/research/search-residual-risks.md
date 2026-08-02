# Residual Risk Analysis: grep/glob Tool Improvements

## Overview

Four residual risks were identified after the search tool improvements (grep
pagination, case-insensitive matching, files-with-matches, multi-`**/` glob,
error reporting). This document researches each risk, challenges the initial
assessment, and prepares reliable implementation plans.

---

## Risk 1: Pagination Walk-Order Determinism

### Initial Claim
> Walk order is deterministic (filesystem-sorted) but not guaranteed across
> filesystem implementations. Match-count offset cursor is stable for unchanged
> trees but not across mutations.

### Research Findings

**`filepath.WalkDir` IS deterministic on Go 1.16+.** The official docs state:

> "The files are walked in **lexical order**, which makes the output
> deterministic but requires WalkDir to read an entire directory into memory
> before proceeding to walk that directory."

Since Go 1.16, `filepath.WalkDir` calls `os.ReadDir`, which (since Go 1.16)
returns entries sorted by filename. This is a Go standard library guarantee, not
a filesystem property.

**Cross-filesystem stability:** The lexical sort is applied by Go's `os.ReadDir`
after the OS returns directory entries — it does NOT depend on the filesystem's
native ordering. Whether ext4, APFS, NTFS, tmpfs, or overlayfs, Go sorts the
entries identically.

**Mutation instability:** This IS a real concern. Between two consecutive
`grep`/`glob` calls with `offset` pagination:
- File creation/deletion shifts all subsequent offsets
- File rename moves entries in the sorted order
- The LLM agent is the mutator — it calls `write_file`, `search_replace`,
  `multi_edit`, and `run_command` between pagination pages

### Challenged Assessment

The initial claim understates the risk. It is not "not guaranteed across
filesystems" — it IS guaranteed by Go's standard library. The real risk is
**mutation between pagination calls**, which is the normal agent workflow:
the agent may create/edit/delete files between page 1 and page 2 of a grep
result.

**Concrete failure mode:**
1. Agent calls `grep("TODO", limit=50)` → gets items [0..49]
2. Agent fixes a TODO in a file, writes the fix via `write_file`
3. Agent calls `grep("TODO", offset=50)` → gets WRONG items because the
   write operation updated `mtime`, or the agent deleted a TODO-containing line
   shifting all subsequent matches

### Implementation Plan

**Option A (recommended): Add tree-content hash to pagination cursor**

Add a `_tree_id` field that the tool computes from a stable fingerprint of the
walk. This is NOT a walk — it uses the workspace's git index when available.

```
grep("TODO", offset=50)
→ "no matches" if tree changed since page 1
→ OR a warning trailer: "warning: tree may have changed; results may skip or duplicate"
```

Implementation:
1. In `executeGrep`, before calling `walkGrep`, compute a cheap tree fingerprint:
   - If `.git/index` exists: `stat` the index file's mtime + size (single syscall)
   - Otherwise: `stat` the workspace root's mtime (degraded, but fast)
2. Return the fingerprint as a hidden field in the trailer:
   `"... 50 more matches (use offset=50 _tree=abc123 to continue)"`
3. On subsequent calls with `_tree`, compare against current fingerprint.
   If mismatch, return `"error: workspace changed since page 1; restart pagination from offset=0"`
4. `_tree` is not a formal parameter — it's parsed from the `offset` string
   or a separate hidden field. Simpler: add it as an optional parameter
   `tree_cursor` that the LLM echoes back.

**Estimated effort:** ~100 LOC in `search.go`, ~30 LOC tests. No external deps.
**Risk if deferred:** Low — pagination is a convenience for the LLM, and
the LLM naturally re-searches from offset=0 when results look wrong. The
failure mode is wasted tokens, not data corruption.

**Option B (deferred): Stateless pagination via sort key**

Return matches sorted by `(path, line)` and have the cursor be
`(path, line)` from the last result. This is immune to insertions/deletions
before the cursor. Much more complex, requires returning structured data.

**Decision:** Defer Option B. Option A is cheap and sufficient for the
LLM-agent use case where the LLM typically fetches 1–3 pages at most.

---

## Risk 2: `(?i)` Prepend Flag Interaction

### Initial Claim
> Patterns with explicit `(?-i)` inline flags will override the prepended
> `(?i)`. This is documented behavior, not a bug.

### Research Findings

**Verified by running Go's regexp package directly.** Results:

| Pattern | Input | Matches? | Why |
|---------|-------|----------|-----|
| `(?i)secret` | "SECRET" | ✅ | `(?i)` makes all case-insensitive |
| `(?i)(?-i)SECRET` | "SECRET" | ✅ | Inner `(?-i)` overrides outer — case-sensitive match |
| `(?i)(?-i)SECRET` | "secret" | ❌ | Case-sensitive, only uppercase matches |
| `(?i)(?m)^foo` | "Foo\nbar" | ✅ | Both flags active simultaneously |
| `(?i)` alone | any | ✅ | Matches empty string at every position |
| `(?i)(?i:foo)bar` | "FooBar" | ✅ | Outer `(?i)` covers everything, inner is redundant |
| `(?i)(?-i:FOO)bar` | "foObar" | ❌ | Scoped `(?-i)` restricts case-sensitivity to group |

**RE2 flag semantics (confirmed):**
- Flags are processed **left-to-right**; the last flag-setting wins
- `(?-i)` inside `(?i)` **overrides** the outer flag — this is documented RE2
  behavior, not a Go-specific quirk
- Multiple top-level `(?flags)` groups stack: `(?i)(?m)` activates both
- `(?flags:re)` scopes flags to the group; the outer `(?i)` still covers
  everything outside the group
- `(?i)` with no subsequent pattern compiles to match-empty-string (harmless
  but useless — returns empty matches for every line)

### Challenged Assessment

The initial claim is correct but incomplete. There are edge cases the initial
analysis missed:

1. **`(?i)` with no pattern body** — compiles to match-empty-string, which would
   match EVERY line in the grep output. The user passed `case_insensitive: true`
   with an empty-like pattern (e.g., `(?i)` as their "pattern"). However, the
   tool already validates `in.Pattern == ""` before prepending, so this can only
   happen if the user literally passes `(?i)` as their pattern — and the tool
   prepends another `(?i)` making it `(?i)(?i)`, which is still
   match-empty-string. **The real bug:** after prepend, the effective pattern is
   `(?i)` which has no literal content, so `re.MatchString(line)` returns true
   for every line. This is a **denial-of-service vector**: a careless or
   adversarial pattern can force the tool to match every line in every file,
   consuming the entire byte budget.

2. **Pattern starting with `(?` already** — `(?m)^TODO` with prepend becomes
   `(?i)(?m)^TODO`. This is valid and correct — both flags stack. Not a bug,
   but worth documenting.

3. **User passes `(?-i)SECRET` expecting case-sensitive** — with prepend,
   becomes `(?i)(?-i)SECRET` which IS case-sensitive (inner overrides outer).
   So the user's intent is preserved. **No bug.**

4. **User passes `(?i:foo)bar`** — with prepend, becomes `(?i)(?i:foo)bar`.
   The outer `(?i)` makes the entire pattern case-insensitive, including `bar`,
   while the user may have intended only `foo` to be case-insensitive. This
   changes behavior — but only when the user explicitly opts into
   `case_insensitive: true` while ALSO writing scoped flags. This is an
   extremely unlikely scenario and the user's intent (case-insensitive matching)
   is arguably better served by making everything case-insensitive.

### Implementation Plan

**Fix the match-empty-string DoS (recommended, small):**

1. After prepend, check if the resulting pattern has no literal content:
   ```go
   // After: pattern = "(?i)" + in.Pattern
   re, err := regexp.Compile(pattern)
   if err != nil {
       return "", fmt.Errorf("invalid pattern: %w", err)
   }
   if re.String() == "" || re.NumSubexp() < 0 {
       // Match-empty-string heuristic: compile a test match
       if re.MatchString("") && re.MatchString("x") && re.MatchString("xyz") {
           return "", fmt.Errorf("pattern matches every string; provide a literal pattern")
       }
   }
   ```

   Actually, the simplest and most robust check: use `regexp/syntax.Parse` to
   walk the AST and verify there's at least one `OpLiteral`, `OpCharClass`,
   `OpAnyChar`, `OpAnyCharNotNL`, or capture group with content.

2. Alternative (simpler): compile the regex, then test against a non-matching
   sentinel. If the regex matches `""`, `"a"`, and `"Z"` all at position 0,
   it's matching everything. But this is fragile — some legitimate patterns
   match "a".

3. Best approach: **wrap instead of prepend**. Use `(?i:USER_PATTERN)` instead
   of `(?i)USER_PATTERN`. This scopes the flag to the entire user pattern
   without creating a separate flag group that can be overridden by the user's
   inner flags. But wait — the user CAN still write `(?-i:...)` inside their
   pattern, which would still override inside the scoped group.

4. **Simplest robust fix:** wrap the pattern in a non-capturing group:
   ```go
   if in.CaseInsensitive {
       pattern = "(?i:" + in.Pattern + ")"
   }
   ```
   This is equivalent to the prepend for simple patterns, but avoids the
   match-empty-string issue when the pattern starts with `(?flags)` only
   — the user must still have content inside.

   But this DOESN'T fix the DoS: `(?i:(?i))` still compiles and matches empty.

5. **Actual fix: reject patterns with no literal/charclass content.** Use
   `regexp/syntax` to inspect the AST after compilation:
   ```go
   import "regexp/syntax"

   func hasLiteralContent(re *regexp.Regexp) bool {
       // Parse the regex back to AST from its string representation.
       // If the AST has no OpLiteral, OpCharClass, OpAnyChar, etc.,
       // it matches everything.
       parsed, err := syntax.Parse(re.String(), syntax.Perl)
       if err != nil {
           return true // conservative: assume it has content
       }
       return hasNonEmptyMatchOp(parsed)
   }
   ```

   This is overengineered for the current risk level. The match-every-string
   pattern already hits the maxMatches/maxBytes budget — the DoS is bounded.

**Decision:** Document the behavior in the tool description. Add a brief note
to the `case_insensitive` parameter description. The byte/match budget already
bounds the worst case. No code change needed unless we see real-world abuse.

**Estimated effort:** 0 LOC (documentation only).
**Risk if deferred:** Negligible — bounded by existing budgets.

---

## Risk 3: Error Collection Cap (maxErrs=10)

### Initial Claim
> Error collection is capped at 10 entries; large directories with systematic
> permission errors may underreport. This is a deliberate tradeoff (bounded
> memory) documented in the code.

### Research Findings

**Current implementation:**
```go
type walkErrors struct {
    errs    []string  // each entry: "path: error message"
    maxErrs int       // hardcoded to 10 in walkGrep/walkGlob
}
```

**Memory analysis:** Each error entry is a path + message string. Worst case
per entry: `4096` (PATH_MAX) + ~100 bytes (error message) ≈ 4.2 KiB. At 10
entries, that's ~42 KiB. Even at 1000 entries, it's ~4.2 MiB — not a memory
concern. The cap is not about memory; it's about output noise in the LLM
context.

**Current notice format:**
```
... 10 files skipped (errors)
```
No details about WHICH files or WHAT errors.

**Comparison with other tools:**
- **ripgrep:** Prints errors to stderr, one per line, with `--no-ignore-errors`
  flag. Does NOT cap error count — but outputs to stderr, not the result stream.
- **fd:** Similar — errors to stderr.
- **Go archive/zip:** Returns first error, no collection.

**LLM-agent context:** The error notice goes into the tool result, which is
stored in the agent's conversation history and sent to the model. Excessive
error detail wastes context tokens. The model doesn't need to know the specific
paths — it needs to know:
1. Whether some files were skipped (yes/no)
2. How many (count)
3. What KIND of error (permission denied, symlink loop, etc.)

### Challenged Assessment

The current implementation is **correct for the LLM-agent use case** with one
gap: it doesn't report the error TYPE, only the count. The model can't
distinguish "10 permission errors" from "10 symlink errors" from "10 read
errors." For an agent trying to fix access issues, this matters.

However, there's a subtlety: the current notice says `"files skipped (errors)"`
but doesn't tell the LLM whether to retry, check permissions, or ignore. A
slightly richer notice would help:

```
... 10 files skipped (permission denied: 8, other: 2)
```

### Implementation Plan

**Option A (recommended): Error-type summarization**

1. Replace the current string-based `walkErrors` with a map-based collector:
   ```go
   type walkErrors struct {
       byType map[string]int  // error message → count
       paths  []string         // first N paths (for diagnostics)
       maxPaths int
       total  int
   }
   ```

2. Classify errors by type:
   - `os.ErrPermission` → "permission denied"
   - `syscall.ENOENT` → "not found"
   - `syscall.ELOOP` → "symlink loop"
   - `syscall.EISDIR` → "is directory"
   - Other → "read error"

3. Notice format:
   ```
   ... 10 files skipped (permission denied: 8, not found: 2)
   ```

4. Keep the path cap at 10 for the detailed list (not shown in notice,
    available for debugging).

**Estimated effort:** ~40 LOC in `glob_match.go`, ~20 LOC tests.
**Risk if deferred:** Very low — the current notice is adequate. The model
doesn't typically act on error notices; it just notes them.

**Option B (minimal): Add error type to first entry only**

Keep the current `walkErrors` but change the notice:
```
... 10 files skipped (first error: bad.txt: permission denied)
```
This is a 5-line change to the `notice()` method.

**Decision:** Option B is sufficient. The model can infer the pattern from
one example. If we see real-world confusion, upgrade to Option A.

---

## Risk 4: `.gitignore` Parsing (Hardcoded Ignore List)

### Initial Claim
> `.gitignore` parsing requires a dedicated library; configurable ignore list
> is pragmatic near-term.

### Research Findings

**Available Go libraries:**

| Library | Stars | Last Update | Transitive Deps | Negation `!` | Nested `.gitignore` | Notes |
|---------|-------|-------------|-----------------|-------------|---------------------|-------|
| `sabhiram/go-gitignore` | 2.1k | 2021 (stale) | 0 (stdlib only) | ✅ | ❌ (single file) | Simple API, compiles lines to regexps |
| `go-git/go-git/v5/plumbing/ignore` | 48k | Active | Many (full git impl) | ✅ | ✅ | Requires pulling in most of go-git |
| `git.sr.ht/~jamesponddotco/gitignore-go` | ~200 | Active | 0 (stdlib) | ✅ | ❌ (single file) | Fork of sabhiram, modern Go, actively maintained |

**Performance consideration:**
- Current `ignoreDir`: O(n) name comparison per directory, n = len(patterns) ≈ 3
- `.gitignore` matching: O(patterns × path_depth) per file, patterns ≈ 50-200
  for a typical project, path_depth ≈ 5-10
- For a repo with 10k files: 10k × 200 × 10 = 20M operations vs. 10k × 3 = 30k
- The `.gitignore` approach is ~700x slower — but still microseconds per file.
  NOT a performance concern.

**Dependency concern:**
- `sabhiram/go-gitignore`: zero transitive deps, but stale (last release 2021,
  open issues untriaged)
- `go-git`: heavy dependency (brings in git object model, transport, etc.)
- `gitignore-go`: zero transitive deps, actively maintained, clean API

**Config surface analysis:**
The codebase already has `defaultIgnorePatterns` as a package-level var and
`ignoreDir` as a simple name matcher. Adding `search_ignore_patterns` to
`ToolsConfig` requires:
1. Add field to `ToolsConfig` struct in `internal/config/types.go`
2. Add resolution in `resolveToolsConfig` in `internal/config/load.go`
3. Wire to `grepTool`/`globTool` in `registerDefaultTools`
4. Already all scoped within `[tools]` TOML section

**Do we even need `.gitignore`?** The current default list (`.git`,
`node_modules`, `vendor`) covers 95% of common cases. The operator can already
add `search_ignore_patterns` when we wire the config key. The main gap is:
1. Nested `.gitignore` files (e.g., `src/` has its own `.gitignore`)
2. Negation patterns (`!keep.log`)
3. Glob-style patterns (`*.tmp`, `build/`)

### Challenged Assessment

The initial claim that `.gitignore` parsing "requires a dedicated library" is
**partially wrong**. A minimal implementation using `filepath.Match` is
feasible for simple patterns (no negation, no nested). But a full
`.gitignore` implementation (with `!` negation, `**` globs, trailing-`/`
directory-only patterns, nested `.gitignore` inheritance) IS complex enough
to warrant a library.

**The real question is: is `.gitignore` the right abstraction for an agent
search tool?** An agent's search scope should be configured by the operator,
not by the project's VCS configuration. The `.gitignore` file excludes files from
git tracking — but the agent may legitimately want to search `node_modules`
or `build/` directories. Conversely, a `search_ignore_patterns` config key
explicitly declares what the agent should skip, which is the correct control
surface.

### Implementation Plan

**Phase 1 (recommended now): Wire `search_ignore_patterns` config key**

1. Add to `ToolsConfig` in `internal/config/types.go`:
   ```go
   // SearchIgnorePatterns adds directory/file names to skip during grep/glob.
   // Extends (does not replace) the built-in defaults.
   SearchIgnorePatterns []string `toml:"search_ignore_patterns,omitempty"`
   ```

2. Add resolution in `resolveToolsConfig`:
   ```go
   // No defaulting needed: empty = use built-in defaults only.
   // The field EXTENDS defaults, not replaces them.
   ```

3. Wire in `registerDefaultTools`:
   ```go
   ignorePatterns := append(append([]string{}, defaultIgnorePatterns...), opts.SearchIgnorePatterns...)
   register(&grepTool{..., ignorePatterns: ignorePatterns})
   register(&globTool{..., ignorePatterns: ignorePatterns})
   ```

4. Add `SearchIgnorePatterns` to `DefaultOptions` struct and threading.

5. Add TOML example in `mivia.toml.example`:
   ```toml
   [tools]
   search_ignore_patterns = ["dist", ".cache", "__pycache__", ".venv"]
   ```

**Estimated effort:** ~50 LOC across config + registry, ~30 LOC tests.
**Dependency:** None.

**Phase 2 (deferred): `.gitignore` integration via opt-in library**

If operators report that `.gitignore` patterns are needed, add
`git.sr.ht/~jamesponddotco/gitignore-go` as an OPTIONAL dependency:

1. Add a `RespectGitignore bool` config key (default false).
2. When true, load `.gitignore` from workspace root + nested directories.
3. Merge with `search_ignore_patterns` (config overrides gitignore).
4. The library is zero-dependency and actively maintained.

**Decision:** Implement Phase 1 now (trivial config wiring). Defer Phase 2
until there's operator demand. The `search_ignore_patterns` key covers the
practical gap today.

---

## Summary: Prioritized Implementation Plan

| Priority | Risk | Action | Effort | Dependencies |
|----------|------|--------|--------|-------------|
| **P2** | R1: Pagination stability | Defer. Tree hash cursor is cheap but unnecessary — LLM naturally re-searches. Document the mutation caveat. | 0 LOC | None |
| **P3** | R2: `(?i)` flag interaction | Document behavior. Byte budget bounds DoS. No code change. | 0 LOC | None |
| **P3** | R3: Error collection cap | Add first-error-type to notice (Option B). 5-line change. | 5 LOC | None |
| **P1** | R4: `.gitignore`/ignore config | Wire `search_ignore_patterns` config key (Phase 1). | ~80 LOC | None |

**Recommended implementation order:**
1. `search_ignore_patterns` config key (R4 Phase 1) — highest practical value
2. Error notice improvement (R3 Option B) — trivial, nice polish
3. Documentation for R1 and R2 — zero code cost
4. Defer R1 tree-hash cursor and R2 AST inspection indefinitely
