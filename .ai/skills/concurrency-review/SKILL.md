---
name: concurrency-review
description: Review concurrency design for mivia. In-process tasks are the default; OS process farm and os/exec fan-out are not. Report with mivia-report/v1.
triggers:
  - concurrency review
  - race review
  - goroutine review
  - parallel agents architecture
---

# Concurrency Review

## Read First

- `AGENTS.md`
- `.ai/rules/50-concurrency-subagents.md`
- `.ai/templates/agent-report-v1.md`
- Diff or packages named by the user

## Method

1. Map shared state, channels, locks, wait groups, contexts, and cancellation paths.
2. Check races, double-close, leaked goroutines, missing `ctx` propagation, and unlock-on-error paths.
3. Reject default architectures that spawn one OS process per agent task via `os/exec` or shell.
4. External processes are allowed only behind reviewed adapters with timeouts, cancel, and allowlists — not as the concurrency model.
5. Require tests that fail under `-race` or deterministic concurrency fixtures for load-bearing paths.
6. Docs/rules in scope must not recommend one OS process per agent task as the default.

## Rules

- Default architecture is in-process concurrency for `mivia` (see `.ai/rules/50-concurrency-subagents.md`).
- `os/exec` / subprocess is an adapter boundary, not the agent fan-out primitive.
- No free-form Output heading; use `mivia-report/v1`.
- Severity never gates approval.

## Required Report

Always emit `mivia-report/v1` from `.ai/templates/agent-report-v1.md`.

Result semantics:

- `PASS` — no race/leak/cancel gap; architecture uses in-process tasks by default; tests cover load-bearing paths.
- `BLOCK` — race, leaked worker, cancel bug, or OS-process-per-agent default remains.
- `PARTIAL` — useful findings but race suite or gated runtime proof incomplete.
- `NOT_RUN` — plan only or review could not start.

```md
ReportFormat: mivia-report/v1
Skill: concurrency-review
Result: PASS|BLOCK|PARTIAL|NOT_RUN
Scope: <exact files/packages>
Baseline: <branch/commit/diff>
Summary: <one sentence>

| ID | Severity | Status | File:Line | Finding | Required Fix | Required Test | Mutation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| none | none | closed | none | none | none | none | none |

| Command | Result | Notes |
| --- | --- | --- |
| none | NOT_RUN | none |

ResidualRisk: none|<short exact risk>
NextAction: none|<exact task>
```
