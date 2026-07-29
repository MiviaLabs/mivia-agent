# Allowlist & Environment Variable Configuration Refactor Plan

## Status: In Progress (Phase 1-2 Complete, Phase 3-5 Remaining)

## Current State

Both program allowlists and environment variable allowlists are hard-coded:

### Program Allowlist (`DefaultAllowlist` in `internal/tools/tools.go`)
~160 hard-coded program names. Can be overridden at construction via `DefaultOptions.RunAllowlist`,
but `configureChatWorkspace` in `chat_repl.go` never passes it — so only built-in defaults apply.

### Env Var Allowlist (`isAllowedEnvVar` in `internal/tools/run.go`)
Hard-coded switch statement with ~25 exact matches and 3 prefix-based rules (`LC_`, `XDG_`, `GIT_`, `NODE_`).
No configuration surface at all. Misses many essential build-time variables.

### Problems
1. **No TOML config support** — no `[tools]` section in `mivia.toml` for allowlists, blocklists, or policies.
2. **No CLI flags** — no `--allow-program`, `--deny-program`, `--no-default-allowlist`, etc.
3. **No override propagation** — `configureChatWorkspace` ignores the config entirely.
4. **No blocklist** — only allowlists exist; no deny/block override mechanism.
5. **No tool-level toggles** — individual tools cannot be disabled independently.
6. **Env var gaps** — `GOPRIVATE`, `CGO_ENABLED`, `RUST_BACKTRACE`, `NODE_PATH`, `PIP_INDEX_URL`, `CC`, `CXX`, etc. are all blocked.

---

## Bugs Found in Completed Phases (Fix Before Continuing)

### P0 — Behavioral Regressions

#### B1: GIT_* and NODE_* prefix vars now blocked (regression from old isAllowedEnvVar)

**Files:** `internal/tools/run.go:296-299` (DefaultEnvAllowlistPrefixes) vs `run.go:378-385` (old isAllowedEnvVar)
**Source:** Phase 2 audit (#2/#3), Phase 3 audit (#6), validated ✅

The old `isAllowedEnvVar()` allowed all `GIT_*` vars (except `GIT_TOKEN*`) and all `NODE_*` vars (except `NODE_OPTIONS` and `NODE_PRESERVE_SYMLINKS`). The new `DefaultEnvAllowlistPrefixes` only has `LC_` and `XDG_`. `GIT_` and `NODE_` prefixes are entirely absent.

**Fix:** Add `"GIT_"` and `"NODE_"` to `DefaultEnvAllowlistPrefixes`.

#### B2: DisableTools comparison is case-sensitive — silently ignores user config

**File:** `internal/tools/tools.go:436-445`
**Source:** Phase 3 audit (#1), validated ✅

The `disabled` map stores user-provided tool names as-is. Tool names are lowercase-underscore (`read_file`, `grep`). Mixed-case TOML values silently fail to disable.

**Fix:** Normalize with `strings.ToLower()` when building the `disabled` map.

### P1 — Correctness Bugs

#### B3: CCX is a bogus environment variable (typo)

**File:** `internal/tools/run.go:285`
**Source:** Phase 2 (#1), Phase 3 (#7), validated ✅

**Fix:** Remove `"CCX"` from `DefaultEnvAllowlist`.

#### B4: SecretPathExceptions appends globally (test pollution)

**File:** `internal/tools/tools.go:412`
**Source:** Phase 3 (#8), validated ✅

`DefaultSecretPathExceptions = append(DefaultSecretPathExceptions, opts.SecretPathExceptions...)` mutates a package-level global. Each call to `NewDefaultRegistry` accumulates exceptions.

**Fix:** Use a fresh copy instead of appending to the global.

#### B5: Package-level filterEnv() bypasses user config (latent)

**File:** `internal/tools/run.go:262-263`
**Source:** Phase 3 (#5), validated ✅

`filterEnv()` creates a zero-valued `runCommandTool{}` with nil fields, always falling back to deprecated `isAllowedEnvVar()`.

**Fix:** Remove the wrapper or have it use resolved env sets.

#### B6: resolveToolsConfig doesn't propagate RedactToolArgs default (latent)

**File:** `internal/config/load.go:206-221`
**Source:** Phase 1 (#3), validated ✅

**Fix:** Add default propagation for `RedactToolArgs`.

#### B7: resolveToolsConfig ignores all 9 slice-based fields (no validation)

**File:** `internal/config/load.go:206-221`
**Source:** Phase 1 (#2), Phase 3 (#2), validated ✅

**Fix:** Add basic validation (e.g. `RunAllowlist` + `RunAllowlistOnly` mutual exclusion).

#### B10: inspect_agent (singular) dead entry in subagent restricted registry

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

### Phase 4: Additional test coverage (estimated: 1 day)

- `parseSuffixNum` unit tests (edge cases: empty, no prefix, non-numeric, overflow)
- `hasContent` unit tests (empty messages, system-only, mixed content)
- `emit()` dual-delivery test (both OnEvent and EventBus)
- `PruneMessagesKeepTurns` budget edge case (system prompt exceeds maxTokens)
- `resolveEnvAllowlist` resolution order tests

### Phase 5: CLI integration (estimated: 1 day)

- Wire `--allow-program`, `--deny-program`, `--no-default-allowlist` into `configureChatWorkspace`
- Wire `--allow-env-var`, `--deny-env-var` into env allowlist resolution
- Integration test: flag parsing + config loading + tool registration
- Test with `--no-default-allowlist` to verify tightest-possible lock-down

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
