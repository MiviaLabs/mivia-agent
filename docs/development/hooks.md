# Development Hooks

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
| `make docs-check` | OWNERS + unique H1 |
| `make semgrep` | Agent standards scan |
| `make test` | `go test ./...` |
| `make race` | `go test -race ./...` |
| `make build` | Build binary `mivia` |

## Pre-commit

- `verify_agent_config.py`
- `secret_scan.py --staged`
- **`file-size-check`** — staged files must be ≤ **500 KiB**
- **`check_go_structure.py --staged`** — Go file/function LOC limits (see below)
- docs ownership when docs staged
- `gofmt` on staged Go
- `git diff --check`
- contract tests (hooks, guard, docs, secrets, semgrep rules, **go-structure**)
- Semgrep on staged files

## Pre-push

- Full config + **`file-size-check --tracked`** (all tracked files ≤ 500 KiB)
- **`check_go_structure.py --all`** (full tree; hard failures block push)
- Secret scan (tracked / range) + docs ownership
- Full Semgrep
- `gofmt -l`, `go test`, `go vet`, `go build -o mivia ./cmd/mivia`

## Structure limits (anti-spaghetti)

Policy: `.ai/policy/go-structure.json` · rules: `.ai/rules/30-go-standards.md`

| Limit | Soft | Hard |
|-------|------|------|
| Prod `.go` file LOC | 500 | 800 |
| Test file LOC | 800 | 1200 |
| Function LOC | 80 | 120 |
| Any staged/tracked file bytes | — | 500 KiB |

Grandfathered oversized files cannot grow past baseline `maxLines`. Lower baseline after splits; never raise it to silence the gate.

```bash
make structure-check   # file-size + go structure + contract tests
```

## Post-commit

Writes `.ai/runs/last-commit.sha` only. No network.

## Agent tool hooks

`.agents/hooks.json`, `.claude/settings.json`, and `.codex/hooks.json` run
`scripts/run_agent_hook_guard.sh` to block verification bypass.

Policy: `.ai/policy/agent-hook-bypass.json`.

## Bypass

Forbidden. Fix the failing gate or report the blocker.
