---
name: verify-change
description: Mechanical verification of a scoped mivia change. Run focused tests, vet, static gates, and report with mivia-report/v1. Use after implementation or before merge claims.
triggers:
  - verify change
  - verify this
  - run verification
  - pre-merge verify
---

# Verify Change

## Read First

- `AGENTS.md`
- `.mivia/templates/agent-report-v1.md`
- `.mivia/quality/contracts/project-runtime.yaml` (relevant contracts only)
- Diff scope named by the user

## Method

1. Confirm exact scope (packages/files) and baseline (branch/commit/diff).
2. Map scope to contracts in `.mivia/quality/contracts/project-runtime.yaml`.
3. Run the narrowest verifiers first: package tests, then `go vet`, then contract verifiers.
4. Record every command with result. Do not invent metrics.
5. Any failed verifier, missing test, or unrun required gate is a gap row.

## Rules

- Binary under test is `mivia` (product CLI name only).
- Do not bypass git hooks.
- Do not use Semgrep suppressions.
- Severity never gates approval; open gaps block `PASS`.

## Required Report

Always emit `mivia-report/v1` from `.mivia/templates/agent-report-v1.md`.

Result semantics:

- `PASS` — all required verifiers for scope ran green; no open gap rows; `ResidualRisk: none`.
- `BLOCK` — failed verifier, missing test, or fixable gap remains.
- `PARTIAL` — useful evidence but a named gated dependency remains.
- `NOT_RUN` — plan only or verification could not start.

```md
ReportFormat: mivia-report/v1
Skill: verify-change
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
