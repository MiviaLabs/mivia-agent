# Allowlist Configuration Refactor Plan

## Current State

The `run_command` tool has a hard-coded global `DefaultAllowlist` in `internal/tools/tools.go` that lists ~160
allowlisted programs. It can be overridden at construction time via `DefaultOptions.RunAllowlist`, but:

1. **No TOML config support** — there is no `[tools]` section in `mivia.toml` to declare additional allowed programs,
   denied programs, or execution policies.
2. **No CLI flags** — no `--allow-program`, `--deny-program`, `--no-default-allowlist` etc.
3. **No override propagation** — `configureChatWorkspace` in `chat_repl.go` never passes `RunAllowlist` from
   config, so the default is always used.
4. **No blocklist** — there is a `isAllowedEnvVar` allowlist for environment variables, but for program execution
   only an allowlist exists — no deny/block override mechanism.
5. **No tool-level capability toggles** — individual tools (`run_command`, `read_file`, `search`, `fetch_url`)
   cannot be enabled/disabled independently.

## Goals

1. **TOML section `[tools]`** in `mivia.toml`:
   ```toml
   [tools]
   # Extend the built-in default allowlist (default: union of built-in + custom)
   run_allowlist = ["docker", "kubectl", "pulumi"]

   # Replace the entire default allowlist (ignore built-in list)
   run_allowlist_only = ["git", "make", "go"]

   # Block programs that would otherwise be allowed (takes precedence over allow)
   run_blocklist = ["rm", "sudo", "docker"]

   # Per-tool enable/disable
   disable_tools = ["search", "fetch_url", "extract"]

   # Timeout per tool category (seconds)
   default_timeout = 300
   tool_timeout = { run_command = 600, fetch_url = 30, search = 15 }

   # Output size limits
   max_read_bytes = 262144
   max_write_kb = 500
   max_output_bytes = 200000
   max_list_dir_entries = 500
   ```

2. **CLI flags**:
   ```
   --allow-program <name>     # Append to allowlist (repeatable)
   --deny-program <name>      # Append to blocklist (repeatable)
   --no-default-allowlist     # Start with empty allowlist (only explicit --allow-program entries)
   --disable-tool <name>      # Disable a built-in tool entirely (repeatable)
   ```

3. **Resolution order** (from least to most specific):
   ```
   Built-in DefaultAllowlist
     → TOML run_allowlist (appended)
       → TOML run_allowlist_only (replaces default)
         → TOML run_blocklist (removed after union)
           → --allow-program flags (appended)
             → --deny-program flags (removed after union)
               → --no-default-allowlist (start empty, only explicit --allow-program)
   ```

## Implementation Steps

### Phase 1: Config types and resolution (estimated: 2-3 days)

1. **Add `ToolsConfig` to `internal/config/types.go`**:
   ```go
   type ToolsConfig struct {
       RunAllowlist      []string        `toml:"run_allowlist"`
       RunAllowlistOnly  []string        `toml:"run_allowlist_only"`
       RunBlocklist      []string        `toml:"run_blocklist"`
       DisableTools      []string        `toml:"disable_tools"`
       DefaultTimeout    int             `toml:"default_timeout"`
       ToolTimeout       map[string]int  `toml:"tool_timeout"`
       MaxReadBytes      int             `toml:"max_read_bytes"`
       MaxWriteKB        int             `toml:"max_write_kb"`
       MaxOutputBytes    int             `toml:"max_output_bytes"`
       MaxListDirEntries int             `toml:"max_list_dir_entries"`
   }
   ```

2. **Add `Tools` field to `config.File`**:
   ```go
   type File struct {
       // ... existing fields
       Tools ToolsConfig `toml:"tools"`
   }
   ```

3. **Add `Tools` field to `config.Resolved`** with resolved values.

4. **Implement resolution in `config.Load()`** — merge defaults, TOML overrides, env overrides.

### Phase 2: Tool registry construction (estimated: 2-3 days)

1. **Refactor `NewDefaultRegistry`** to accept `DefaultOptions` with resolved config instead of
   relying on the global `DefaultAllowlist`.

2. **Implement `resolveAllowlist` function** that implements the resolution order above.

3. **Wire tool enable/disable** — skip registration of tools in `DisableTools`.

4. **Thread config through `configureChatWorkspace`** and all test helpers.

5. **Update `configureChatWorkspace`** in `internal/cli/chat_repl.go` to pass the resolved config.

### Phase 3: CLI flags (estimated: 1-2 days)

1. **Add flags to CLI root** in `internal/cli/root.go`:
   ```go
   var (
       allowPrograms    []string
       denyPrograms     []string
       noDefaultAllow   bool
       disableTools     []string
   )
   ```

2. **Wire flags into `Resolved`** before tool registry construction.

3. **Update `flagValue` or equivalent** in `chat_repl.go` and other entry points.

### Phase 4: Blocklist for env vars (estimated: 1 day)

1. **Add `env_allowlist` / `env_blocklist` to `ToolsConfig`** so users can override
   the `isAllowedEnvVar` allowlist.

2. **Add `--allow-env-var` / `--deny-env-var` CLI flags**.

### Phase 5: Tests (estimated: 2 days)

1. **Unit tests** for `resolveAllowlist` resolution order and edge cases.
2. **Integration tests** for flag parsing + config loading + tool registration.
3. **Test with `--no-default-allowlist`** to verify tightest-possible lock-down.

## Acceptance Criteria

- [ ] `mivia.toml` `[tools]` section is parsed and respected
- [ ] `--allow-program git` works (repeatable)
- [ ] `--deny-program rm` works (repeatable, overrides allow)
- [ ] `--no-default-allowlist` results in an empty allowlist
- [ ] `--disable-tool search` removes the search tool
- [ ] Resolution order is: builtin < TOML-append < TOML-replace < TOML-block < flag-allow < flag-deny < flag-no-default
- [ ] Existing tests pass without modification (backward compatible)
- [ ] All 18 internal packages pass `go test -race`
- [ ] Environment variable allowlist is also configurable

## Files to Modify

| File | Change |
|------|--------|
| `internal/config/types.go` | Add `ToolsConfig` struct, add to `File` and `Resolved` |
| `internal/config/load.go` | Resolve tools config in `Load()` |
| `internal/config/defaults.go` | Default values for tools config |
| `internal/tools/tools.go` | `resolveAllowlist()`, tool disable logic |
| `internal/tools/ALLOWLIST-REFACTOR-PLAN.md` | This plan |
| `internal/cli/chat_repl.go` | Thread resolved config → registry |
| `internal/cli/root.go` | CLI flag definitions |
| `internal/cli/chat.go` | Pass flags through |
| `internal/cli/interactive_session_test.go` | Update test helpers |
