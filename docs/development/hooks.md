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
- docs ownership when docs staged
- `gofmt` on staged Go
- `git diff --check`
- contract tests (hooks, guard, docs, secrets, semgrep rules)
- Semgrep on staged files

## Pre-push

- Full config + secret scan (tracked) + docs ownership
- Full Semgrep
- `gofmt -l`, `go test`, `go vet`, `go build -o mivia ./cmd/mivia`

## Post-commit

Writes `.ai/runs/last-commit.sha` only. No network.

## Agent tool hooks

`.agents/hooks.json`, `.claude/settings.json`, and `.codex/hooks.json` run
`scripts/run_agent_hook_guard.sh` to block verification bypass.

Policy: `.ai/policy/agent-hook-bypass.json`.

## Bypass

Forbidden. Fix the failing gate or report the blocker.
