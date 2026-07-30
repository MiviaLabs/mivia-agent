---
name: secure-change
description: Security and privacy review for a scoped mivia change. Check authz, secrets, injection, path safety, logging of sensitive data, and fail-closed defaults. Report with mivia-report/v1.
triggers:
  - secure change
  - security review
  - privacy review
  - threat check
  - secrets
  - auth
---

# Secure Change

## Read First

- `AGENTS.md`
- `.mivia/rules/10-security-privacy.md`
- `.mivia/templates/agent-report-v1.md`
- Diff scope named by the user
- `docs/security/` owned paths when present

## Method

1. Define trust boundaries for the change (inputs, FS paths, env, subprocess, config).
2. Check deny-by-default authz and no IDOR/path escape across tenant or repo roots.
3. Scan for hardcoded secrets, token logging, prompt/payload persistence, and world-writable modes.
4. Reject shell metachar/`sh -c` execution paths; require allowlisted parameterized exec.
5. Confirm tests cover at least one negative security path per new guard.
6. Run available static gates (`semgrep/agent-standards.yml`, secret scan scripts when present).
7. For new parsers, configuration decoders, or tool schemas that accept untrusted structured input, require malformed, empty, oversized, and duplicate-input tests. Run a bounded fuzz target when a deterministic target is practical, and report when it is not.

## Rules

- Brand is MiviaLabs. CLI is `mivia`.
- Do not instruct hook bypass.
- Do not add Semgrep suppressions in product code.
- Treat PII and secrets as toxic in logs, traces, fixtures, and error strings.
- Severity never gates approval.

## Required Report

Always emit the compact `mivia-report/v1` from `.mivia/templates/agent-report-v1.md`.

Result semantics:

- `PASS` — no concrete security/privacy gap in scope; required negative tests present.
- `BLOCK` — any secret leak path, authz hole, injection, or missing security test remains.
- `PARTIAL` — useful findings but gated tool or incomplete source access.
- `NOT_RUN` — plan only or review could not start.
