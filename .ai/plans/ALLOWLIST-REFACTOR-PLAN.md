# Allowlist & Environment Variable Configuration Refactor Plan

## Status: Phase 1-4 Complete, Phase 5 Remaining

## Current State

Both program allowlists and environment variable allowlists are now configurable via `mivia.toml`:

### Program Allowlist (`DefaultAllowlist` in `internal/tools/tools.go`)
~160 hard-coded program names. Overridable via TOML `[tools]` section (`run_allowlist`, `run_allowlist_only`, `run_blocklist`) or `DefaultOptions` at construction. `configureChatWorkspace` in `chat_repl.go` now threads all `ToolsConfig` fields through.

### Env Var Allowlist (`DefaultEnvAllowlist` + `DefaultEnvAllowlistPrefixes` in `internal/tools/run.go`)
Configurable via TOML `[tools]` section (`env_allowlist`, `env_allowlist_only`, `env_blocklist`). `resolveEnvAllowlist()` implements layered resolution with wildcard prefix support.

### CLI Flags
Not yet implemented — `--allow-program`, `--deny-program`, `--no-default-allowlist`, `--disable-tool`, `--allow-env-var`, `--deny-env-var` are still TODO.

---

## Known Issues

### K1: Dispatch timeout race condition (infrastructure)
When `dispatch_tasks` is used with `timeout_seconds`, a task may time out at the orchestrator level even though its side effects (file writes) have fully completed. The timeout signal kills the reporting channel before the sub-agent can report success, resulting in a "timed_out" status despite all work being on disk and passing tests.

**Observed:** Task `write_plan_tests_1` (timeout=180s) timed out but all 37 requested tests were written and committed. The second task `write_plan_tests_2` (same timeout) completed normally.

**Impact:** Low — no data loss, but misleading status. Mitigation: use generous timeouts for file-writing tasks, or add a post-timeout verification step.

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

## Completed (Phases 1-4)

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

## Remaining (Phase 5)

### Phase 5: CLI flags & integration (estimated: 1.5 days)

Add flags to CLI root in `internal/cli/root.go`:
```go
var (
    allowPrograms    []string
    denyPrograms     []string
    noDefaultAllow   bool
    disableTools     []string
    allowEnvVars     []string
    denyEnvVars      []string
)
```

Wire flags into `Resolved.Tools` before tool registry construction.
Wire through `configureChatWorkspace`.
Integration test: TOML config → flag parsing → tool registration.
Document the `[tools]` section in `mivia.toml` example.

## Acceptance Criteria

- [x] `mivia.toml` `[tools]` section is parsed and respected (Phase 1)
- [x] `env_allowlist`, `env_allowlist_only`, `env_blocklist` control `isAllowedEnvVar` (Phase 2)
- [x] Resolution order is verified by unit tests (Phase 4)
- [x] Existing tests pass without modification (backward compatible)
- [x] All 18 internal packages pass `go test -race`
- [x] `parseSuffixNum`, `hasContent`, `emit` dual-delivery have unit tests (Phase 4)
- [x] `PruneMessagesKeepTurns` budget edge case tests (Phase 4)
- [ ] `--allow-program git` works (repeatable) — Phase 5
- [ ] `--deny-program rm` works (repeatable, overrides allow) — Phase 5
- [ ] `--no-default-allowlist` results in empty allowlist — Phase 5
- [ ] `--disable-tool search` removes the search tool — Phase 5
