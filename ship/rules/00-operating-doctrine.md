# Operating Doctrine (Shipped Edition)

## Canonical Source Order

1. `.ai/` — project control surface (rules, skills, policy, quality).
2. System / tool instructions.
3. Task prompt.

## Scope Control

- Before implementation, read the relevant plan/task docs.
- Stay inside the named task, file, or package boundary unless the user expands scope.
- Do not implement product code outside an agreed task or explicit user request.
- Preserve existing docs and user changes unless the task requires editing them.
- Prefer the smallest change that satisfies the acceptance criteria.

## Documentation-First Work

- Code changes that alter behavior, flags, config, security posture, or public API must update the canonical doc for that topic.
- If implementation reveals a task split, update the task before writing the second production unit.
- Completion reports name changed files, verification run, and residual risk. Do not claim "done" without verification status.

## Idempotency

- Writers, generators, init/update commands, and importers must be rerunnable with no diff for the same inputs.
- Every writer needs an idempotency test.
