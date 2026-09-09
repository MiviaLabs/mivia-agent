# Development Hooks

> This page is about the repository's **Git** hooks - the ones the agent must
> never bypass. For mivia's own lifecycle layer, which runs your scripts around
> the agent's tool calls, see [lifecycle-hooks.md](lifecycle-hooks.md). The two
> run in opposite directions and share only a word.

Install once per clone:

```bash
make install-hooks
```

This sets `core.hooksPath=.githooks`.

## Make targets

| Target | Purpose |
|--------|---------|
| `make verify` | Full local gate (no network) |
| `make pre-commit` | Run pre-commit hook |
| `make pre-push` | Run pre-push hook |
| `make secret-scan` | Scan tracked files for secrets |
| `make docs-check` | OWNERS + unique H1 + provider-docs↔registry |
| `make semgrep` | Agent standards scan |
| `make test` | `go test ./...` |
| `make race` | `go test -race ./...` |
| `make build` | Build binary `mivia` |

## Pre-commit

- `verify_agent_config.py`
- `secret_scan.py --staged`
- **`file-size-check`** - staged files must be ≤ **500 KiB** (binary databases `*.db`, `*.sqlite`, `*.sqlite3` are exempt: opaque blobs that cannot be split)
- **`check_go_structure.py --strict --worktree`** - Go file/function LOC limits (see below), run against a materialized copy of the staged tree
- docs ownership when docs staged
- chat-sync wire vocabulary check when Markdown or the contract changed
- `gofmt` on staged Go
- `git diff --check`
- Semgrep on staged files
- area-specific invariant tests, auto-selected by which packages are staged (TUI, agent, tools, provider, events, hub, coordinator, storage, config, and more)
- mutation sweep is **deferred to pre-push** (a broad staged change could run past ten minutes here)

## Pre-push

- Full config + **`file-size-check --tracked`** (all tracked files ≤ 500 KiB)
- **`check_go_structure.py --strict --all`** (full tree; hard failures block push)
- Secret scan (tracked / range). The range is per pushed ref, from the `<local ref> <local sha> <remote ref> <remote sha>`
  lines git hands the hook on stdin (read before any child process runs; the
  `run_with_timeout` supervisor forwards its stdin to the hook). With no ref lines the hook
  prints `pre-push: no ref lines on stdin; sweeping HEAD` and falls back to HEAD.
  The mutation sweep uses the same ranges.
- docs ownership (`check_docs_ownership.py`)
- The contract test suites for the Git hooks, the agent hook guard, secret scan, docs ownership, gate scripts, and **provider-docs↔registry** (`test_check_provider_docs.py`, which runs `check_provider_docs.py` itself)
- Full Semgrep
- `gofmt -l`, `go test ./...`, `go vet ./...`, `go build -o /dev/null ./cmd/mivia` (no binary is produced or kept)
- Mutation sweep over the full push range

## Structure limits (anti-spaghetti)

Limits are enforced by the pre-commit and pre-push hooks (500/800 LOC for files, 80/120 LOC for functions).

| Limit | Soft | Hard |
|-------|------|------|
| Prod `.go` file LOC | 500 | 800 |
| Test file LOC | 800 | 1200 |
| Function LOC | 80 | 120 |
| Any staged/tracked file bytes | - | 500 KiB |

Grandfathered oversized files cannot grow past baseline `maxLines`. Lower baseline after splits; never raise it to silence the gate.

```bash
make structure-check   # file-size + go structure (no contract tests)
```

## Post-commit

Writes `.mivia/runs/last-commit.sha` only. No network.

## Agent tool hooks

`.agents/hooks.json`, `.claude/settings.json`, and `.codex/hooks.json` run
`scripts/run_agent_hook_guard.sh` to block verification bypass.

Guard: blocked verification flags and corrective messages are enforced by the hook.

## Hook layers

Three hook systems live in this repo. They are **not** redundant - each covers a
different agent runtime, and removing one removes protection for the agents that
depend on it.

| Layer | Consumed by | Fires on | Purpose |
|---|---|---|---|
| Git hooks (`.githooks/`) | every local `git` | commit, push, commit-msg | config validation, secret scan, structure limits, Semgrep, tests |
| Agent tool hooks (`.agents/`, `.claude/`, `.codex/`) | Claude Code, Codex, agents CLI | that harness's own pre-tool event | block verification bypass |
| Lifecycle hooks (`.mivia/mivia.toml`, `~/.mivia/mivia.toml`) | the `mivia` binary | `PreToolUse`, `PostToolUse`, `Stop` | gates and reactions on mivia's own tool calls |

Which layers fire depends on who is driving:

- **Claude Code or Codex is the agent** → layers 1 and 2. Layer 3 does not exist
  in those runtimes; they never call mivia's dispatcher.
- **mivia is the agent** → layers 1 and 3. Layer 2's guard is wired into other
  harnesses' settings files and does not see mivia's Go runtime.

Layer 1 is the only one that is unconditional, which is why it is the one that
must never be bypassed - the others can be absent by construction.

**Bypass policy is written once.** `.mivia/policy/agent-hook-bypass.json` is
read by all three: the Git hooks, `scripts/agent_hook_guard.py`, and this repo's
own `PreToolUse` hook at `.mivia/hooks/run-command-guard.py`. Tighten the JSON;
never fork the patterns into a copy, or one layer quietly keeps enforcing an
older rule. The same script also reads
`.mivia/policy/destructive-commands.json`, which is a different question - "is
this about to lose work" rather than "is this skipping verification" - and so is
a different file with its own corrective message.

See [`lifecycle-hooks.md`](lifecycle-hooks.md) for layer 3 in full.

## Bypass

Forbidden. Fix the failing gate or report the blocker.
