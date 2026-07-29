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
