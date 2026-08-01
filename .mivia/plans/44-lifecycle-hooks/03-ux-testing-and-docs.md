# Phase 3 — UX, Testing, and Documentation (P2)

**Status**: Planned
**Items**: R5, R6, R7, R8
**Depends on**: Phase 0 (analysis), Phase 1 (output framing — R5 should verify framing propagates to subagents)

## Problem

Four gaps in the operational experience and test coverage:

1. **No integration test verifying subagent hook propagation**: The mechanism (Policy func-field copy via `parentPolicy()`) is confirmed correct, but there is no test that verifies a subagent dispatcher reads the same `sessionHookState` and fires the same hooks.
2. **No way to prune or revoke hook trust**: The trust store (`~/.mivia/hook-trust.json`) is append-only. Stale entries accumulate with no removal path.
3. **No documented `Stop` hook example**: `Stop` is observation-only and fires only in the interactive TUI. A turn-logging example would demonstrate the pattern and its limitation.
4. **Script-content trust limitation undocumented for agent contexts**: The "editing the script body does not revoke trust" design is documented as sound for Codex, but in mivia's agent-with-exec context it has a real attack surface via `run_command`.

---

## R5 — Integration Test: Subagent Hook Propagation

### Scope

- `internal/subagents/multi_step_test.go` or new test file
- `internal/cli/hooks_runner.go` — verify `currentHookSession()` reads correctly from subagent goroutines

### Tasks

#### 3.1 — Write integration test for subagent PreToolUse hook inheritance

File: `internal/subagents/` (alongside existing integration tests)

Test scenario:
1. Configure a mock `PreInvokeHook` on the parent dispatcher.
2. Dispatch a `multi_step` subagent task.
3. The subagent's scoped dispatcher (created via `NewToolDispatcher(reg, h.parentPolicy())`) should inherit the hook func.
4. When the subagent invokes a tool, the parent's `PreInvokeHook` should fire.
5. If the hook denies, the subagent's tool call should be blocked with `status: "blocked"`.

Existing tests to reference:
- `TestScopedSubagentDispatcherInheritsHookFuncs` (confirms Policy func-field copy)
- `TestConcurrentInvocationKeepsItsGateWhileAnotherHookRuns` (confirms context-scoped re-entry guard)
- `TestHookReentryIntoTheDispatcherDoesNotRecurse` (confirms hooks package boundary)

#### 3.2 — Verify `sessionHookState` reads correctly from subagent goroutines

The hook funcs installed by `hookPolicyFuncs` (`internal/cli/hooks_runner.go`) call `currentHookSession()` to get `gatedRunnable()`. This reads from `sessionHookState`, a process-global `atomic.Pointer[hookSession]`. Verify:
- A subagent's tool call goroutine reads the same session state as the parent.
- `/hooks trust` from the UI goroutine is visible to a subsequent tool call.
- A headless session correctly suppresses all hooks in subagent goroutines.

---

## R6 — `/hooks untrust` or `/hooks prune` Command

### Scope

- `internal/cli/hooks_command.go` — add `untrust` or `prune` subcommand
- `internal/hooks/trust.go` — add `Prune` or `Remove` method to `Store`
- `internal/cli/hooks_command_test.go` — tests for the new command

### Design Considerations

**Option A: `/hooks untrust <number>`** — removes a specific confirmation
- Removes the matching `(Source, Hash)` record from the store.
- The hook's status reverts to `pending` or `hash-changed` on the next session.
- The user must re-confirm to reactivate.

**Option B: `/hooks prune`** — removes all records that don't match current groups
- Compares store records against the current session's resolved groups.
- Removes any record whose `(Source, Hash)` doesn't match an active group.
- Safe: doesn't affect currently-active hooks.

**Option C: `/hooks reset`** — removes all confirmations
- Nuclear option. Every hook becomes `pending` on the next session.
- Useful for a "clean slate" after config changes.

**Recommendation**: Implement Option A (`/hooks untrust <number>`) as the primary, with Option B (`/hooks prune`) as a secondary. Option C is too blunt for normal use.

### Tasks

#### 3.3 — Add `Remove(group Group) error` to `Store`

File: `internal/hooks/trust.go`

```go
// Remove deletes the trust record for a group, if one exists.
// The group's status reverts to pending or hash-changed on the next resolve.
func (s *Store) Remove(group Group) error {
    if s.loadErr != nil {
        return fmt.Errorf("refusing to write the hook trust store: %w", s.loadErr)
    }
    for i, record := range s.records {
        if record.Source == group.Source && record.Hash == group.Hash {
            s.records = append(s.records[:i], s.records[i+1:]...)
            return s.write()
        }
    }
    return nil // no record to remove
}
```

#### 3.4 — Add `/hooks untrust` handler to `handleSlashHooks`

File: `internal/cli/hooks_command.go`

Parse `/hooks untrust <number>` alongside existing `/hooks trust <number>`. The number is the same index from `/hooks` listing.

#### 3.5 — Add tests

- Test that `/hooks untrust` removes the record and the hook's status reverts.
- Test that untrusting a non-existent record is a no-op.
- Test that untrusting from a corrupt store is refused (same pattern as `Confirm`).
- Test that the `/hooks` listing shows the reverted status.

#### 3.6 — (Optional) Add `/hooks prune`

If implementing, prune all store records that don't match any current group's `(Source, Hash)`.

---

## R7 — `Stop` Hook Example for Turn Logging

### Scope

- New example hook script
- Documentation in `docs/development/lifecycle-hooks.md`

### Tasks

#### 3.7 — Create example Stop hook script

```toml
# Example: log turn metadata
[[hooks]]
event   = "Stop"

  [[hooks.handlers]]
  type       = "command"
  argv       = ["./hooks/turn-log.sh"]
  timeout    = 5
  on_timeout = "allow"
```

```sh
#!/bin/sh
# hooks/turn-log.sh — appends turn metadata to a JSONL log file.
# Reads MIVIA_SESSION_ID, MIVIA_WORKSPACE_ROOT from environment.
# Reads turn metadata from stdin JSON.

LOG_DIR="${MIVIA_WORKSPACE_ROOT:-.}/.mivia/runs"
mkdir -p "$LOG_DIR"
LOG_FILE="$LOG_DIR/turns.jsonl"

# Read stdin, append as a log entry with session ID
SESSION="${MIVIA_SESSION_ID:-unknown}"
ENTRY="$(cat)"
printf '{"session":"%s","event":"stop","timestamp":"%s","payload":%s}\n' \
  "$SESSION" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$ENTRY" >> "$LOG_FILE"
exit 0
```

#### 3.8 — Document with caveat

Add to lifecycle-hooks.md alongside the example:
> **Limitation**: `Stop` fires only in the interactive TUI. The `--plain` REPL and `-p` one-shot do not publish a turn-end event, so this hook is silent in those contexts. Turn logging is most useful in interactive sessions where no external observability exists.

---

## R8 — Document Script-Content Trust Limitation for Agent Contexts

### Scope

- `docs/development/lifecycle-hooks.md` — add to the "Trust" section

### Tasks

#### 3.9 — Add threat-model note to lifecycle-hooks.md

Add after the existing "What trust does and does not cover" section:

> **Agent-with-exec context**: The above boundary ("editing the script body does not revoke trust") is designed for harnesses where the model cannot execute arbitrary commands. In mivia's context, the agent has `run_command`, which can reach files outside the workspace (subject to the run allowlist). A hook script at `~/.mivia/hooks/gate.sh` is resolved against the config file's directory, which is outside the workspace write surface — but the agent can modify it via shell redirection, heredoc, or other `run_command` techniques. Once rewritten, the tampered script fires on the next `PreToolUse` **with its existing trust confirmation intact**.
>
> This is a documented, accepted limitation. The mitigations are:
> 1. Hook scripts are user-authored files under the user's own version control at `~/.mivia/`.
> 2. The hook output framing (structural delimiter) limits the blast radius of a compromised script to advisory context.
> 3. The `PreToolUse` deny reason reaches the model verbatim — a compromised script can block tools, but it cannot silently pass them through (the deny reason is attributed).
> 4. The `run_command` allowlist limits which programs the agent can invoke, reducing the attack surface for script modification.

## Verification

- `go test ./internal/...` — all existing + new tests pass
- `make verify` — full offline gate
- `make docs-check` — OWNERS + unique H1
- Manual: test `/hooks untrust <n>` in an interactive session
- Manual: configure the turn-log Stop hook and verify it writes to `.mivia/runs/turns.jsonl`

## Exit Criteria

- Integration test for subagent hook propagation passes (R5)
- `/hooks untrust <number>` command functional with tests (R6)
- Stop hook example documented with caveat (R7)
- Script-content trust limitation documented (R8)
