# Allowlist & Environment Variable Configuration Refactor Plan

## Status: Phase 1-3 Complete, Phase 4-5 Remaining

## Current State

Both program allowlists and environment variable allowlists are now configurable via `mivia.toml`:

### Program Allowlist (`DefaultAllowlist` in `internal/tools/tools.go`)
~160 hard-coded program names. Overridable via TOML `[tools]` section (`run_allowlist`, `run_allowlist_only`, `run_blocklist`) or `DefaultOptions` at construction. `configureChatWorkspace` in `chat_repl.go` now threads all `ToolsConfig` fields through.

### Env Var Allowlist (`DefaultEnvAllowlist` + `DefaultEnvAllowlistPrefixes` in `internal/tools/run.go`)
Configurable via TOML `[tools]` section (`env_allowlist`, `env_allowlist_only`, `env_blocklist`). `resolveEnvAllowlist()` implements layered resolution with wildcard prefix support.

### CLI Flags
Not yet implemented — `--allow-program`, `--deny-program`, `--no-default-allowlist`, `--disable-tool`, `--allow-env-var`, `--deny-env-var` are still TODO.

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
**Fix applied:** Replaced global mutation with per-tool local copy threading. All tools (`readFileTool`, `listDirTool`, `grepTool`, `globTool`, `writeFileTool`, `searchReplaceTool`, `runCommandTool`) carry their own `secretPathExceptions` and `secretPathPatterns` fields. `isSecretPath()` and `secretPathInArgv()` accept parameters instead of reading globals. Fallback to globals when nil. See `tools.go`, `read.go`, `write.go`, `search.go`, `run.go`.

#### ✅ B5: Package-level filterEnv() bypasses user config
**Fix applied:** Removed the package-level `filterEnv()` wrapper entirely. Callers in tests now use properly configured `runCommandTool`. See `run.go:261-263`.

#### ✅ B6: resolveToolsConfig doesn't propagate RedactToolArgs default
**Fix applied:** Added `RedactToolArgs` default propagation in `resolveToolsConfig()` in `load.go`.

#### ✅ B7: resolveToolsConfig ignores all 9 slice-based fields (no validation)
**Fix applied:** Added mutual exclusion validation — `RunAllowlist` + `RunAllowlistOnly` and `EnvAllowlist` + `EnvAllowlistOnly` are mutually exclusive (prefers `*Only` variant, clears the other). See `load.go:248-257`.

#### ✅ B8: RedactToolArgs has two independent sources with OR logic
**Fix applied:** Consolidated to single source of truth (`PrivacyConfig` → package atomic). Removed `RedactToolArgs` from `DefaultOptions` struct. `runCommandTool` now uses only `RedactToolArgs()` (the package atomic). `chat_repl.go` only sets via `tools.SetRedactToolArgs(res.Privacy.RedactToolArgs)`. See `privacy.go`, `chat_repl.go`, `tools.go:452`.

#### ✅ B9: Env blocklist only matches exact names, not prefixes (undocumented)
**Fix applied:** Wildcard (`*`) prefix support was already implemented in `resolveEnvAllowlist()` — now documented in `types.go` field comments for `EnvAllowlist`, `EnvAllowlistOnly`, and `EnvBlocklist`.

#### ✅ B10: inspect_agent (singular) dead entry in subagent restricted registry
**Fix applied:** Removed `"inspect_agent"` (singular) from the blocked map in `multi_step.go:211`. Only `"inspect_agents"` (plural, the real tool name) remains.

---

## Completed (Phases 1-3)

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

## Remaining (Phases 4-5)

### Phase 4: Additional test coverage (estimated: 1.5 days)

- `parseSuffixNum` unit tests (edge cases: empty, no prefix, non-numeric, overflow)
- `hasContent` unit tests (empty messages, system-only, mixed content)
- `emit()` dual-delivery test (both OnEvent and EventBus)
- `PruneMessagesKeepTurns` budget edge case (system prompt exceeds maxTokens)
- **`resolveEnvAllowlist` resolution order tests** (default → `env_allowlist_only` replaces → `env_allowlist` appended → `env_blocklist` removed → keyword blocklist applied last)
- **`GIT_*` / `NODE_*` prefix regression tests** — verify env vars like `GIT_DIR`, `GIT_SSH_COMMAND`, `NODE_ENV`, `NODE_DEBUG` are allowed (keyword-blocked ones like `GIT_TOKEN` remain blocked)
- **`DisableTools` case-insensitivity test** — verify `"Read_File"`, `"GREP"`, `"Run_Command"` all match regardless of casing
- **`SecretPathExceptions` global isolation test** — verify multiple `NewDefaultRegistry` calls don't leak exceptions
- **`resolveToolsConfig` slice field validation test** — verify `RunAllowlist` + `RunAllowlistOnly` mutual exclusion is enforced
- **`inspect_agents` blocked in subagent `restrictedRegistry()` test** — verify `inspect_agents` tool is excluded (dead `inspect_agent` entry removed)
- **`filterEnv()` removed test** — verify callers use properly configured `runCommandTool`
- **`RedactToolArgs` single-source test** — verify `PrivacyConfig` is the sole source of truth

### Phase 5: CLI integration (estimated: 1.5 days)

- Add CLI flags to `internal/cli/root.go`:
  ```go
  --allow-program     (repeatable)
  --deny-program      (repeatable)
  --no-default-allowlist
  --disable-tool      (repeatable)
  --allow-env-var     (repeatable)
  --deny-env-var      (repeatable)
  ```
- Wire flags into `Resolved.Tools` before tool registry construction
- Wire through `configureChatWorkspace`
- **Integration test: TOML config → flag parsing → tool registration** — end-to-end for allowlist, blocklist, disable_tools
- Document the `[tools]` section in `mivia.toml` example

## Acceptance Criteria

- [x] `mivia.toml` `[tools]` section is parsed and respected (Phase 1)
- [x] `env_allowlist`, `env_allowlist_only`, `env_blocklist` control `isAllowedEnvVar` (Phase 2)
- [ ] `--allow-program git` works (repeatable) — Phase 5
- [ ] `--deny-program rm` works (repeatable, overrides allow) — Phase 5
- [ ] `--no-default-allowlist` results in empty allowlist — Phase 5
- [ ] `--disable-tool search` removes the search tool — Phase 5
- [ ] Resolution order is verified by unit tests — Phase 4
- [x] Existing tests pass without modification (backward compatible) ✅
- [x] All 18 internal packages pass `go test -race` ✅
- [ ] `parseSuffixNum`, `hasContent`, `emit` dual-delivery have unit tests — Phase 4nt (singular) dead entry in subagent restricted registry

**File:** `internal/subagents/multi_step.go:211`
**Source:** External agent finding, git history confirmed ✅

Originally (commit `eead25de`), `inspect_agents` was entirely missing from `restrictedRegistry()` — subagents could freely inspect parent runs. Partially fixed in `49b4c6e2` by adding `"inspect_agents": true`, but `"inspect_agent": true` (singular — dead code, never matches any tool) was left behind.

**Fix:** Remove the dead `"inspect_agent": true` entry.

### P2 — Design Clarity

#### B8: RedactToolArgs has two independent sources with OR logic

**Files:** `internal/config/types.go:42,66`, `tools/tools.go:450`, `cli/chat_repl.go:40,128`
**Source:** Phase 1 (#1), Phase 3 (#3), validated ✅

**Fix:** Consolidate to a single source of truth (recommend `PrivacyConfig`).

#### B9: Env blocklist only matches exact names, not prefixes (undocumented)

**File:** `internal/tools/run.go:316-349`
**Source:** Phase 2 (#4), validated ✅

**Fix:** Document this behavior in the `EnvBlocklist` field comment.

---

## Completed (Phases 1-2)

### Phase 1 ✅ — Config types
- `ToolsConfig` struct added to `internal/config/types.go` with fields for all allowlist, blocklist, and policy settings
- `Tools` field added to `config.File`
- Defaults for `ToolsConfig` added to `internal/config/defaults.go`
- Resolution in `config.Load()` — merges TOML with defaults, writes resolved values to `Resolved.Tools`

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

### Phase 3 ✅ — Tool registry wiring
- `DefaultOptions` extended with `EnvAllowlist`, `EnvBlocklist`, `EnvAllowlistOnly`, `DisableTools`
- `NewDefaultRegistry` applies `DisableTools` filtering
- `runCommandTool` receives env allow/block lists and uses `resolveEnvAllowlist()` at runtime
- `configureChatWorkspace` in `chat_repl.go` threads the resolved config through

## Remaining (Phase 3-5)

### Phase 3: CLI flags (estimated: 1-2 days)

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

### Phase 4: Additional test coverage (estimated: 1.5 days)

- `parseSuffixNum` unit tests (edge cases: empty, no prefix, non-numeric, overflow)
- `hasContent` unit tests (empty messages, system-only, mixed content)
- `emit()` dual-delivery test (both OnEvent and EventBus)
- `PruneMessagesKeepTurns` budget edge case (system prompt exceeds maxTokens)
- **`resolveEnvAllowlist` resolution order tests** (default → `env_allowlist_only` replaces → `env_allowlist` appended → `env_blocklist` removed → keyword blocklist applied last)
- **`GIT_*` / `NODE_*` prefix regression tests** — verify env vars like `GIT_DIR`, `GIT_SSH_COMMAND`, `NODE_ENV`, `NODE_DEBUG` are allowed (keyword-blocked ones like `GIT_TOKEN` remain blocked)
- **`DisableTools` case-insensitivity test** — verify `"Read_File"`, `"GREP"`, `"Run_Command"` all match regardless of casing
- **`SecretPathExceptions` global isolation test** — verify multiple `NewDefaultRegistry` calls don't accumulate exceptions (no test pollution)
- **`resolveToolsConfig` slice field validation test** — verify `RunAllowlist` + `RunAllowlistOnly` mutual exclusion is enforced
- **`inspect_agents` blocked in subagent `restrictedRegistry()` test** — verify `inspect_agents` tool is excluded (and `inspect_agent` singular dead entry removed)
- **`filterEnv()` package-level wrapper test** — verify it uses the deprecated fallback (or remove the wrapper entirely and verify all paths use resolved config)

### Phase 5: CLI integration (estimated: 1.5 days)

- Wire `--allow-program`, `--deny-program`, `--no-default-allowlist` into `configureChatWorkspace`
- Wire `--allow-env-var`, `--deny-env-var` into env allowlist resolution
- **Integration test: TOML config → flag parsing → tool registration** — end-to-end for allowlist, blocklist, disable_tools, env allow/block
- **Integration test: `--no-default-allowlist`** — verify tightest-possible lock-down (empty program allowlist)
- **Integration test: `DisableTools` case-insensitivity** — verify mixed-case disable values work through the full TOML+flag pipeline
- **Integration test: `GIT_*` env vars flow through** — verify `run_command` subprocess receives expected env vars from config-resolved allowlist

## Resolution Order

### Program allowlist:
```
Built-in DefaultAllowlist (~160 programs)
  → TOML run_allowlist (appended to default)
    → TOML run_allowlist_only (replaces default entirely)
      → TOML run_blocklist (removed after union)
        → --allow-program flags (appended)
          → --deny-program flags (removed after union)
            → --no-default-allowlist (start empty, only explicit --allow-program)
```

### Env var allowlist:
```
Built-in DefaultEnvAllowlist (~40 vars + prefixes)
  → TOML env_allowlist (appended to default)
    → TOML env_allowlist_only (replaces default entirely)
      → TOML env_blocklist (removed after union)
        → --allow-env-var flags (future)
          → --deny-env-var flags (future)
```

## Acceptance Criteria

- [ ] `mivia.toml` `[tools]` section is parsed and respected (Phase 1 ✅)
- [ ] `env_allowlist`, `env_allowlist_only`, `env_blocklist` control `isAllowedEnvVar` (Phase 2 ✅)
- [ ] `--allow-program git` works (repeatable)
- [ ] `--deny-program rm` works (repeatable, overrides allow)
- [ ] `--no-default-allowlist` results in empty allowlist
- [ ] `--disable-tool search` removes the search tool
- [ ] Resolution order is verified by unit tests
- [ ] Existing tests pass without modification (backward compatible) ✅
- [ ] All 18 internal packages pass `go test -race` ✅
- [ ] `parseSuffixNum`, `hasContent`, `emit` dual-delivery have unit tests
