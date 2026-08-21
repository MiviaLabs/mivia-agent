# Contributing

## Setup

```bash
go env -w GOPRIVATE=github.com/MiviaLabs/*
make install-hooks
make verify
make build
./mivia version
```

Required tools: Go 1.25+, Python 3, Semgrep.

`go env -w GOPRIVATE=github.com/MiviaLabs/*` alone does not authenticate a
private-module fetch. It only tells `go get` to skip the module proxy and
sumdb for paths that match the glob. It carries no credential.

Credential wiring (an SSH `insteadOf` rewrite, or a PAT-backed HTTPS
rewrite) is a deferred follow-up. It lands alongside dropping any future
`go.mod` `replace` directive. Until it lands, a developer who adds a
genuine private MiviaLabs import will hit an unexplained 404 or auth
failure at `go get`. This note exists so that failure is not a mystery.

The setup above assumes developers already hold SSH keys for
`github.com:MiviaLabs/*`. This assumption is unconfirmed with the team;
treat it as an assumption to verify, not a settled fact.

## Workflow

1. Branch from default: `mivia/<short-scope>`
2. Implement with tests
3. Run `make verify` (or narrower gates while iterating)
4. Commit with `type(scope): subject` (scope required)
5. Push only when pre-push is green

## Commit format

Commit messages must follow `type(scope): imperative subject` (enforced by `commit-msg` hook).

```text
type(scope): imperative subject
```

Examples:

```text
feat(cli): add version command
chore(ai): bootstrap agent control surface
fix(hooks): print allowed scopes on commit-msg failure
```

### Types

`feat` `fix` `docs` `chore` `test` `refactor` `build` `ci` `perf` `style` `revert` `security`

### Scopes (required)

| Scope | Use for |
|-------|---------|
| `cli` | cmd/mivia, flags, TUI |
| `agent` | orchestrator, subagents, runtime |
| `hooks` | Git + agent tool hooks |
| `ai` | `.mivia/` rules, skills, doctrines, policy |
| `docs` | `docs/**`, OWNERS |
| `security` | secrets, privacy, authz |
| `quality` | verify scripts, Semgrep, contract tests |
| `build` | Makefile, go.mod, packaging |
| `ci` | GitHub Actions |
| `test` | tests only |
| `deps` | dependency bumps only |
| `release` | versioning / release process |

No `setup` scope. Use `ai` / `hooks` / `quality` / `build` for bootstrap work.

If the hook rejects a message, read the printed **allowed types** and **allowed scopes** lines first, then fix the subject.

## Docs

Edit the path owned in `docs/OWNERS.yaml`. Do not create parallel guides.

## Agents

Follow `AGENTS.md` and `.mivia/`. Humans and agents use the same gates.
