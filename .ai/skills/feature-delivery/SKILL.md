---
name: feature-delivery
description: Deliver one scoped mivia feature end-to-end tests, implementation, verification, and mivia-report/v1 completion report. Brand MiviaLabs; binary mivia.
triggers:
  - feature delivery
  - implement feature
  - deliver this task
  - finish implementation
---

# Feature Delivery

## Read First

- `AGENTS.md`
- `.ai/templates/agent-report-v1.md`
- `.ai/quality/contracts/project-runtime.yaml`
- Task/scope named by the user

## Method

1. Lock scope to one feature slice (production paths + tests). Do not broaden.
2. Write or update tests for success and at least one error/negative path first or alongside code.
3. Implement the smallest change that satisfies the scope.
4. Run focused package tests and `go vet` for touched packages.
5. Run matching contract verifiers from `project-runtime.yaml` when paths match.
6. Apply secure-change checks (secrets, path safety, fail-closed) before claiming done.
7. Emit completion report only for actual progress; no invented metrics.

## Rules

- Product CLI is `mivia`. Brand is MiviaLabs.
- No hook bypass. No Semgrep suppressions. No unresolved drift markers in committed product or agent config.
- Do not default to one OS process per concurrent agent task.
- Severity never gates approval; open gaps block `PASS`.

## Required Report

Always emit `mivia-report/v1` from `.ai/templates/agent-report-v1.md`.

Result semantics:

- `PASS` — scoped feature implemented, verified, gaps closed, ready for requested handoff.
- `BLOCK` — implementation, test, verifier, or security gap remains.
- `PARTIAL` — useful slice landed but named dependency or user decision remains.
- `NOT_RUN` — plan only or delivery could not start.

```md
ReportFormat: mivia-report/v1
Skill: feature-delivery
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
