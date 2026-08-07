# Commit messages: type(scope): subject (≤72 chars, lowercase start after colon)

**Always validate before committing.** Do not guess the format - use the dry-run check.

## Rules (from `.mivia/policy/commit-message.json`)

```
type(scope): lowercase subject under 72 chars, no trailing period
```

| Gate | Max | Why it fails |
|------|-----|-------------|
| Subject length | **72 chars** | `subject is longer than 72 characters` |
| First word after `: ` | **lowercase/digit** | `subject body must start with a lowercase letter` |
| Trailing period | **no** | `subject must not end with a period` |
| Scope | **required** | `scope is required; use type(scope): subject` |

## Required body trailers

`.mivia/policy/commit-message.json` `requiredTrailers` declares which trailers a
type must carry. A `fix` commit needs all three. The commit-msg hook rejects a
missing trailer, and rejects a label with no value.

| Trailer | Value | Why it is a gate |
|---------|-------|------------------|
| `Regression:` | `Test<Name>`, or `none (<reason>)` | The test that fails before the fix and passes after it. |
| `Class:` | `DC-n` from `.mivia/quality/defect-taxonomy.md`, or `none (<reason>)` | Names the recurring defect class. A fix that matches no class must say so, and the class belongs in that document. |
| `Sweep:` | `searched <what>, found <n> further sites`, or `none (<reason>)` | The same-class sweep. This is the gate that stops one class producing a chain of repeat fixes; the history holds chains of 35, 45, and 26 fixes for one class. |

Example body for a fix:

```text
Regression: TestDeliverDeliveryFailedReentry
Class: DC-1
Sweep: searched all terminal states in the delivery machine, found 0 further sites
```

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
before running `git commit`. This check takes <50ms - no semgrep, no tests.

## Common mistakes (and the fix)

| Mistake | Fix |
|---------|-----|
| `feat(cli): TUI revamp...` (uppercase T) | `feat(cli): tui revamp...` |
| `feat(cli): add feature.` (trailing `.`) | `feat(cli): add feature` |
| `feat(cli): a very long subject that exceeds seventy two characters total...` | Shorten to ≤72 chars; put details in commit body |
| `feat(TUI): ...` (bad scope) | `feat(cli): ...` (use `cli` for TUI work) |
| `feat(cli): add feature` (capital `A` = ok, it's lowercase) | Already correct ✅ |
