# Contributing

## Setup

```bash
make install-hooks
make verify
make build
./mivia version
```

Required tools: Go 1.25+, Python 3, Semgrep.

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
