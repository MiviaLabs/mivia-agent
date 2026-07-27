# Commit messages: type(scope): subject (≤72 chars, lowercase start after colon)

**Always validate before committing.** Do not guess the format — use the dry-run check.

## Rules (from `.ai/policy/commit-message.json`)

```
type(scope): lowercase subject under 72 chars, no trailing period
```

| Gate | Max | Why it fails |
|------|-----|-------------|
| Subject length | **72 chars** | `subject is longer than 72 characters` |
| First word after `: ` | **lowercase/digit** | `subject body must start with a lowercase letter` |
| Trailing period | **no** | `subject must not end with a period` |
| Scope | **required** | `scope is required; use type(scope): subject` |

## Allowed types

`feat`, `fix`, `docs`, `chore`, `test`, `refactor`, `build`, `ci`, `perf`, `style`, `revert`, `security`

## Allowed scopes

`cli`, `agent`, `mcp`, `hooks`, `ai`, `docs`, `security`, `quality`, `build`, `ci`, `test`, `deps`, `release`

## Examples

```
feat(cli): add version command
fix(hooks): print allowed scopes on commit-msg failure
docs(docs): document commit scopes in contributing
chore(ai): bootstrap agent control surface
test(hooks): cover invalid scope error output
```

## How to validate (always do this first)

```bash
python3 scripts/git-hooks/check-commit-subject "feat(cli): your subject under 72 chars"
```

If exit code is 0, the subject is valid. If non-zero, fix the reported errors
before running `git commit`. This check takes <50ms — no semgrep, no tests.

## Common mistakes (and the fix)

| Mistake | Fix |
|---------|-----|
| `feat(cli): TUI revamp...` (uppercase T) | `feat(cli): tui revamp...` |
| `feat(cli): add feature.` (trailing `.`) | `feat(cli): add feature` |
| `feat(cli): a very long subject that exceeds seventy two characters total...` | Shorten to ≤72 chars; put details in commit body |
| `feat(TUI): ...` (bad scope) | `feat(cli): ...` (use `cli` for TUI work) |
| `feat(cli): add feature` (capital `A` = ok, it's lowercase) | Already correct ✅ |
