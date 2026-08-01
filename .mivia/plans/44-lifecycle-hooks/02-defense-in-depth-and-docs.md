# Phase 2 — Defense-in-Depth and Documentation (P1)

**Status**: Planned
**Items**: R2, R3, R4
**Depends on**: Phase 0 (analysis)

## Problem

Three gaps in the defense-in-depth posture:

1. **No edit-time formatting feedback**: `gofmt` drift is caught at pre-commit (hard gate), but not at write time. Formatting errors accumulate across a session before being caught.
2. **No mivia-runtime guard on destructive git operations**: The Python guard (`agent_hook_guard.py`) blocks bypass attempts for Claude/Codex, but does not protect mivia's own Go runtime. A `PreToolUse` lifecycle hook would add defense-in-depth within mivia.
3. **Confusing relationship between adapter hooks and lifecycle hooks**: The three hook layers cover different agent runtimes, but this is not documented anywhere. A developer could remove adapter hooks thinking lifecycle hooks supersede them.

## R2 — PostToolUse Hook Example: `gofmt -w` on `.go` File Writes

### Scope

- New example hook script (location TBD — `docs/development/examples/` or `docs/development/lifecycle-hooks.md` inline)
- Documentation in `docs/development/lifecycle-hooks.md`

### Tasks

#### 2.1 — Create example PostToolUse hook script

```toml
# Example: run gofmt on .go file writes
[[hooks]]
event   = "PostToolUse"
matcher = "write_file|search_replace"

  [[hooks.handlers]]
  type       = "command"
  argv       = ["./hooks/gofmt-check.sh"]
  timeout    = 10
  on_timeout = "allow"
```

```sh
#!/bin/sh
# hooks/gofmt-check.sh — runs gofmt on .go files after writes.
# Reads MIVIA_FILE from environment (set by mivia for write_file/search_replace).
# Advisory only: PostToolUse cannot block.

FILE="${MIVIA_FILE:-}"
case "$FILE" in
  *.go) gofmt -w "$FILE" 2>&1 ;;
esac
exit 0
```

Notes:
- `MIVIA_FILE` is set from `hookFileFromInput` (`internal/cli/hooks_runner.go:87`), which extracts the top-level `path` argument from the tool input.
- `on_timeout = "allow"` — this is advisory; a slow format check should not block work.
- The script is a starting point, not a product hook. Users copy and adapt.

#### 2.2 — Add to lifecycle-hooks documentation

Add the example to `docs/development/lifecycle-hooks.md` under a new "Common patterns" section with:
- The TOML config
- The script
- Explanation of `MIVIA_FILE`, `on_timeout = "allow"`, and why this is advisory-only
- Caveat: `gofmt -w` rewrites the file; if the model's view of the file is stale after the hook runs, the next read will see the reformatted content. This is correct behavior — the hook is fixing what the model wrote.

---

## R3 — PreToolUse Lifecycle Hook for Destructive Git Operations

### Scope

- New example hook script
- Documentation in `docs/development/lifecycle-hooks.md`
- Policy file extension (optional: add destructive git patterns to `.mivia/policy/agent-hook-bypass.json`)

### Tasks

#### 2.3 — Create example PreToolUse hook script

```toml
# Example: block destructive git operations
[[hooks]]
event   = "PreToolUse"
matcher = "^run_command$"

  [[hooks.handlers]]
  type       = "command"
  argv       = ["./hooks/block-destructive-git.sh"]
  timeout    = 5
  on_timeout = "block"
```

```sh
#!/bin/sh
# hooks/block-destructive-git.sh — blocks force-push, hard reset, branch -D.
# Reads tool invocation JSON from stdin.

COMMAND="$(python3 -c 'import sys,json; d=json.load(sys.stdin); print(" ".join(d.get("input",{}).get("argv",[])))' 2>/dev/null)"
if echo "$COMMAND" | grep -qE '(git\s+push\s+.*--force|git\s+push\s+-f|git\s+reset\s+--hard|git\s+branch\s+-D|git\s+branch\s+-d\s+-f)'; then
  printf 'Destructive git operation blocked: %s\n' "$COMMAND" >&2
  exit 2
fi
exit 0
```

Notes:
- `on_timeout = "block"` — a hung gate must not be an open gate.
- `matcher = "^run_command$"` — matches only `run_command`, not other tools.
- Exit 2 blocks the call; stderr is the reason the model sees.

#### 2.4 — Extend bypass policy (optional)

Consider adding destructive git patterns to `.mivia/policy/agent-hook-bypass.json` `blockedCommandPatterns`:
- `(?i)\bgit\s+push\b[\s\S]*--force`
- `(?i)\bgit\s+push\s+-f\b`
- `(?i)\bgit\s+reset\s+--hard`

This would protect external harnesses (Claude/Codex) as well, complementing the lifecycle hook for mivia's runtime.

#### 2.5 — Add to lifecycle-hooks documentation

Same section as R2. Explain:
- Why `on_timeout = "block"` (destructive operations must not pass on a hung check)
- The relationship to the existing Python guard (different runtime coverage)
- How to customize the blocked patterns

---

## R4 — Document Adapter Hooks vs Lifecycle Hooks Relationship

### Scope

- `docs/development/hooks.md` — add a new section explaining the three layers
- `docs/development/lifecycle-hooks.md` — add a cross-reference

### Tasks

#### 2.6 — Add "Hook layers" section to hooks.md

Add to `docs/development/hooks.md`:

```
## Hook layers

This repo has three hook systems that protect different surfaces:

| Layer | Consumed by | Fires on | Purpose |
|-------|-------------|----------|---------|
| Git hooks (`.githooks/`) | Every developer's local `git` | commit, push, commit-msg | Config validation, secret scanning, structure limits, Semgrep, tests |
| Agent tool hooks (`.agents/hooks.json`, `.codex/hooks.json`) | Claude Code, Codex CLI, agents CLI | PreToolUse, PermissionRequest, UserPromptSubmit | Block bypass attempts (`--no-verify`, `HUSKY=0`, hook deletion) |
| Lifecycle hooks (`~/.mivia/mivia.toml`) | mivia binary only | PreToolUse, PostToolUse, Stop | User-defined gates on the mivia runtime's own tool calls |

**The layers are NOT redundant.** Each covers a different agent runtime:
- When Claude Code or Codex is the agent, only layers 1 and 2 fire. Layer 3 does not exist in those runtimes.
- When mivia is the agent, layers 1 and 3 fire. Layer 2's Python guard does not protect mivia's own Go runtime.
- Removing any layer removes protection for the agents that depend on it.
```

#### 2.7 — Add cross-reference to lifecycle-hooks.md

Add a note at the top of `docs/development/lifecycle-hooks.md`:
> Lifecycle hooks are one of three hook layers in this repo. See [`hooks.md`](hooks.md) for the full layer breakdown and why each is needed.

## Verification

- `make verify` — full offline gate
- `make docs-check` — OWNERS + unique H1
- Manual: copy example hooks into `~/.mivia/mivia.toml`, run `/hooks`, `/hooks trust`, and verify they fire on the expected tool calls

## Exit Criteria

- Example `gofmt-check.sh` hook documented with TOML config
- Example `block-destructive-git.sh` hook documented with TOML config
- "Hook layers" section in `docs/development/hooks.md` explaining the three layers
- Cross-reference in `docs/development/lifecycle-hooks.md`
- (Optional) destructive git patterns added to `agent-hook-bypass.json`
