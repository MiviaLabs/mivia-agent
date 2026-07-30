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
- `.mivia/templates/agent-report-v1.md`
- `.mivia/quality/contracts/project-runtime.yaml`
- Task/scope named by the user

## Method

1. Lock scope to one feature slice (production paths + tests). Do not broaden.
2. Write or update tests for success and at least one error/negative path first or alongside code.
3. Implement the smallest change that satisfies the scope.
4. Run focused package tests and `go vet` for touched packages.
5. Run matching contract verifiers from `project-runtime.yaml` when paths match.
6. Apply secure-change checks (secrets, path safety, fail-closed) before claiming done.
7. When the change parses or decodes untrusted structured input (for example config, frontmatter, CLI schema, or tool parameters), add malformed, empty, oversized, and duplicate-input cases. Run a bounded fuzz target such as `go test -fuzz=FuzzParse -fuzztime=10s ./affected/package` when a deterministic target is practical; otherwise state why it was not run.
8. Emit completion report only for actual progress; no invented metrics.

## Rules

- Product CLI is `mivia`. Brand is MiviaLabs.
- No hook bypass. No Semgrep suppressions. No unresolved drift markers in committed product or agent config.
- Do not default to one OS process per concurrent agent task.
- Severity never gates approval; open gaps block `PASS`.

## Required Report

Always emit the compact `mivia-report/v1` from `.mivia/templates/agent-report-v1.md`.

Result semantics:

- `PASS` — scoped feature implemented, verified, gaps closed, and ready for requested handoff.
- `BLOCK` — implementation, test, verifier, or security gap remains.
- `PARTIAL` — useful slice landed but named dependency or user decision remains.
- `NOT_RUN` — plan only or delivery could not start.
