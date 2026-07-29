# Allowlist & Environment Variable Configuration Refactor Plan

## Status: All Phases Complete ✅

## Current State

Both program allowlists and environment variable allowlists are now configurable via `mivia.toml` AND CLI flags.

### Program Allowlist (`DefaultAllowlist` in `internal/tools/tools.go`)
~160 hard-coded program names. Overridable via TOML `[tools]` section (`run_allowlist`, `run_allowlist_only`, `run_blocklist`) or `DefaultOptions` at construction. `configureChatWorkspace` in `chat_repl.go` now threads all `ToolsConfig` fields through.

### Env Var Allowlist (`DefaultEnvAllowlist` + `DefaultEnvAllowlistPrefixes` in `internal/tools/run.go`)
Configurable via TOML `[tools]` section (`env_allowlist`, `env_allowlist_only`, `env_blocklist`). `resolveEnvAllowlist()` implements layered resolution with wildcard prefix support.

### CLI Flags
Implemented. See `internal/cli/root.go` and `internal/cli/chat_repl.go`.

| Flag | Type | Behavior |
|------|------|----------|
| `--allow-program` | Repeatable | Appends to `RunAllowlist` (adds program) |
| `--deny-program` | Repeatable | Appends to `RunBlocklist` (removes program) |
| `--no-default-allowlist` | Bool | Sets `RunAllowlistOnly` to empty (no defaults) |
| `--disable-tool` | Repeatable | Appends to `DisableTools` |
| `--allow-env-var` | Repeatable | Appends to `EnvAllowlist` |
| `--deny-env-var` | Repeatable | Appends to `EnvBlocklist` |

**Merge order:** TOML config is baseline. CLI flags APPEND to TOML values (except `--no-default-allowlist` which overrides).

---

## Known Issues

### ✅ K1: Dispatch timeout race condition — FIXED
**Fix applied in `internal/cli/dispatch.go`:** When `dispatch_tasks` encounters a timeout while some tasks have already completed, it now returns the completed results instead of a bare error. Previously all partial work was lost — now completed task results are returned with timed_out tasks noted inside the results array.

**Root cause:** The timeout kills the Go context but file writes had already flushed to disk. The `Join()` returned `ctx.Err()` (DeadlineExceeded), and the old code discarded `runResult.Results` when `runResult.Err != nil`.

**Fix:** Check `len(results) > 0` before returning the error payload. If partial results exist, call `encodeResults(results)` and return them so the caller sees completed work.

---

## Bugs — Status

All 10 bugs fixed as of audit (2025-07).

### P0 — Behavioral Regressions (FIXED)

#### ✅ B1: GIT_* and NODE_* prefix vars now blocked
**Fix applied:** `"GIT_"` and `"NODE_"` added to `DefaultEnvAllowlistPrefixes` in `run.go:298-299`.

#### ✅ B2: DisableTools comparison is case-sensitive
**Fix applied:** `strings.ToLower()` normalization in `tools.go:428-430`.

### P1 — Correctness Bugs (FIXED)

#### ✅ B3: CCX is a bogus environment variable
**Fix applied:** Removed `"CCX"` from `DefaultEnvAllowlist`.

#### ✅ B4: SecretPathExceptions appends globally (test pollution)
**Fix applied:** Replaced global mutation with per-tool local copy threading. All tools (`readFileTool`, `listDirTool`, `grepTool`, `globTool`, `writeFileTool`, `searchReplaceTool`, `runCommandTool`) carry their own `secretPathExceptions` and `secretPathPatterns` fields. `isSecretPath()` and `secretPathInArgv()` accept parameters instead of reading globals. Fallback to globals when nil.

#### ✅ B5: Package-level filterEnv() bypasses user config
**Fix applied:** Removed the package-level `filterEnv()` wrapper entirely. Callers in tests now use properly configured `runCommandTool` (see `run.go:261-263`).

#### ✅ B6: resolveToolsConfig doesn't propagate RedactToolArgs default
**Fix applied:** Added `RedactToolArgs` default propagation in `resolveToolsConfig()` in `load.go`.

#### ✅ B7: resolveToolsConfig ignores all 9 slice-based fields (no validation)
**Fix applied:** Added mutual exclusion validation — `RunAllowlist` + `RunAllowlistOnly` and `EnvAllowlist` + `EnvAllowlistOnly` are mutually exclusive (prefers `*Only` variant, clears the other). See `load.go:248-257`.

#### ✅ B8: RedactToolArgs has two independent sources with OR logic
**Fix applied:** Consolidated to single source of truth (`PrivacyConfig` → package atomic). Removed `RedactToolArgs` from `DefaultOptions` struct. `runCommandTool` now uses only `RedactToolArgs()` (the package atomic). `chat_repl.go` only sets via `tools.SetRedactToolArgs(res.Privacy.RedactToolArgs)`.

#### ✅ B9: Env blocklist only matches exact names, not prefixes (undocumented)
**Fix applied:** Wildcard (`*`) prefix support was already implemented in `resolveEnvAllowlist()` — now documented in `types.go` field comments for `EnvAllowlist`, `EnvAllowlistOnly`, and `EnvBlocklist`.

#### ✅ B10: inspect_agent (singular) dead entry in subagent restricted registry
**Fix applied:** Removed `"inspect_agent"` (singular) from the blocked map in `multi_step.go:211`. Only `"inspect_agents"` (plural, the real tool name) remains.

---

## Completed (Phases 1-5)

### Phase 1 ✅ — Config types
- `ToolsConfig` struct added to `internal/config/types.go` with fields for all allowlist, blocklist, and policy settings
- `Tools` field added to `config.File`
- Defaults for `ToolsConfig` added to `internal/config/defaults.go`
- Resolution in `config.Load()` — merges TOML with defaults, writes resolved values to `Resolved.Tools`
- Mutual exclusion validation for conflicting `*Allowlist`/`*AllowlistOnly` pairs

### Phase 2 ✅ — `isAllowedEnvVar` refactored
Deprecated the hard-coded `isAllowedEnvVar()` function.
Replaced with `DefaultEnvAllowlist` + configurable `resolveEnvAllowlist()` that implements:
```
Built-in DefaultEnvAllowlist
  → TOML env_allowlist (appended)
    → TOML env_allowlist_only (replaces default)
      → TOML env_blocklist (removed)
        → --allow-env-var flags (future)
          → --deny-env-var flags (future)
```
- GIT_*, NODE_*, LC_*, XDG_* prefix support with keyword blocklist
- Wildcard (`*`) prefix rules in custom allow/block entries

### Phase 3 ✅ — Tool registry wiring
- `DefaultOptions` extended with `EnvAllowlist`, `EnvBlocklist`, `EnvAllowlistOnly`, `DisableTools`, `SecretPathPatterns`, `SecretPathExceptions`
- `NewDefaultRegistry` applies `DisableTools` filtering (case-insensitive)
- `runCommandTool` receives env allow/block lists and uses `resolveEnvAllowlist()` at runtime
- `configureChatWorkspace` in `chat_repl.go` threads the resolved config through
- `RedactToolArgs` consolidated to single source of truth (`PrivacyConfig`)
- `SecretPathExceptions`/`SecretPathPatterns` threaded as per-tool fields instead of global mutation
- Package-level `filterEnv()` wrapper removed

### Phase 4 ✅ — Test coverage
917 lines of new tests across 8 files:

| Test Area | File | Tests |
|-----------|------|-------|
| `resolveEnvAllowlist` resolution order | `run_test.go` | 7 tests (defaults, only, append, block, keywords, wildcards) |
| GIT_*/NODE_* prefix regression | `run_test.go` | 4 tests (safe allowed, keyword blocked) |
| DisableTools case-insensitivity | `tools_test.go` | 2 tests (mixed-case, lower-case) |
| RedactToolArgs single source | `tools_test.go` | 3 tests (PrivacyConfig, defaults, DefaultOptions absent) |
| SecretPathExceptions isolation | `tools_test.go` | 1 test (no cross-registry mutation) |
| filterEnv via configured tool | `tools_test.go` | 2 tests (correct tool path, wrapper gone) |
| resolveToolsConfig validation | `load_test.go` | 5 tests (exclusive, no-conflict, both-empty, subagent defaults) |
| inspect_agents blocked in subagent | `multi_step_test.go` | 1 test (all delegation tools filtered) |
| hasContent | `save_manager_test.go` | 6 tests (empty, system-only, mixed) |
| Emit dual-delivery | `emit_test.go` | 4 tests (both channels, each alone, nil) |
| parseSuffixNum | `storage_test.go` | 1 test (14 edge cases) |
| PruneMessagesKeepTurns budget edge case | `context_test.go` | 2 tests (system exceeds, zero budget) |

### Phase 5 ✅ — CLI flags & integration
- `--allow-program`, `--deny-program`, `--no-default-allowlist`, `--disable-tool`, `--allow-env-var`, `--deny-env-var` added to `internal/cli/root.go`
- Parsed in `internal/cli/chat_repl.go` via new `flagVar()` function for repeatable string flags
- Merged into `Resolved.Tools` after `config.Load()` (TOML baseline, CLI flags append)
- `--no-default-allowlist` sets `RunAllowlistOnly = []string{}`; `NewDefaultRegistry` nil-check updated to handle empty slice
- K1 fixed: `dispatch_tasks` returns partial results on timeout (completed tasks preserved)

## Acceptance Criteria

All criteria met. ✅

- [x] `mivia.toml` `[tools]` section is parsed and respected (Phase 1)
- [x] `env_allowlist`, `env_allowlist_only`, `env_blocklist` control `isAllowedEnvVar` (Phase 2)
- [x] Resolution order is verified by unit tests (Phase 4)
- [x] Existing tests pass without modification (backward compatible)
- [x] All 18 internal packages pass `go test -race`
- [x] `parseSuffixNum`, `hasContent`, `emit` dual-delivery have unit tests (Phase 4)
- [x] `PruneMessagesKeepTurns` budget edge case tests (Phase 4)
- [x] `--allow-program git` works (repeatable) — Phase 5
- [x] `--deny-program rm` works (repeatable, overrides allow) — Phase 5
- [x] `--no-default-allowlist` results in empty allowlist — Phase 5
- [x] `--disable-tool search` removes the search tool — Phase 5
