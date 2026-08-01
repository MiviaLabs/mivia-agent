# Phase 3 — UX, Testing, and Documentation (P2)

**Status**: Implemented (2026-08-01). R5, R7, R8 delivered. **R6 is obsolete** - see below.

> **Written before Phase 1.** Phase 1 removed the hook trust store, so parts of this
> plan describe machinery that no longer exists. Each item was re-checked against
> the code before implementation; the corrections are recorded inline rather than
> silently followed or silently skipped.
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

**Corrected after Phase 1.** `gatedRunnable()` is gone with the gate, `/hooks trust`
is gone with the trust store, and a headless session no longer suppresses anything -
so two of the three original sub-points asserted behaviour that was deliberately
removed. What survives is the part that was always the point:

- A subagent's tool call goroutine reads the same session state as the parent.
- A subagent's tool call fires the parent's `PreToolUse` hook, sees `Kind == Tool`,
  and is BLOCKED when that hook denies - a gate a subagent escapes is not a gate.
- `PostToolUse` context reaches the *nested* model, framed in
  `<lifecycle-hook-output>` exactly as the root loop's is.
- A handler with no parent dispatcher runs no hooks rather than calling a nil func.

Delivered in `internal/subagents/multi_step_hooks_test.go`. The tests assert on
what the nested model was actually sent (tool-role message content), not on
`multi_step`'s return value - that is the mock's final text and would have passed
while the hook text was being dropped.

---

## R6 — `/hooks untrust` or `/hooks prune` Command — **OBSOLETE, NOT IMPLEMENTED**

This item has no subject any more. Phase 1 removed hook trust confirmation
entirely: there is no `~/.mivia/hook-trust.json`, no `hooks.Store`, no `Record`,
no `Decision`, and no `/hooks trust`. `internal/hooks/trust.go` and
`internal/hooks/hash.go` were deleted.

The problem statement it answered - "the trust store is append-only, stale
entries accumulate with no removal path" - is solved more completely than any
`untrust` subcommand would have solved it: the store that accumulated the stale
entries does not exist. A hook stops running when you delete its `[[hooks]]`
table, which is the same file you added it to.

Nothing here should be revived unless a confirmation model returns, and if one
ever does, this design (Option A `untrust <n>`, Option B `prune`, Option C
`reset`) is a reasonable starting point for it.

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

**Corrected during implementation.** The script above is close but wrong in two
ways, both fixed in the shipped version: there is no `MIVIA_TURN_ID` environment
variable (the env carries `MIVIA_HOOK_EVENT`, `MIVIA_TOOL`, `MIVIA_FILE`,
`MIVIA_SESSION_ID`, `MIVIA_WORKSPACE_ROOT` and nothing else - `turn_id` is in the
stdin JSON), and an empty stdin makes `printf` emit `"payload":` followed by
nothing, so one payload-less turn corrupts the whole JSONL file. Both paths were
executed before the example was written down.

#### 3.8 — Document with caveat

Add to lifecycle-hooks.md alongside the example:
> **Limitation**: `Stop` fires only in the interactive TUI. The `--plain` REPL and `-p` one-shot do not publish a turn-end event, so this hook is silent in those contexts. Turn logging is most useful in interactive sessions where no external observability exists.

---

## R8 — Document Script-Content Trust Limitation for Agent Contexts

### Scope

- `docs/development/lifecycle-hooks.md` — add to the "Trust" section

### Tasks

#### 3.9 — Add threat-model note to lifecycle-hooks.md

**Rewritten for the current design.** The draft below assumes hook scripts live at
`~/.mivia/hooks/`, "outside the workspace write surface", and that a tampered
script "fires with its existing trust confirmation intact". Since Phase 1 there
are no confirmations, and since Phase 1's project-hook support a hook script
routinely lives *inside* the workspace, where `write_file` reaches it directly -
so the shipped section separates the two reach surfaces instead of treating
`run_command` as the only one, and names moving a hook to the user config as the
actual mitigation.

Draft as originally written, kept for the reasoning:

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
