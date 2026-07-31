# 09.03 — Verification and closeout

**Goal:** Close the agent program only after docs, examples, ownership, and implementation contracts agree.
**Depends on:** [02](02-parser-backed-examples.md) and plan `08`.

## Checks

- Refresh `.mivia/INDEX.md`, the program overview, and all active plan links.
- Run `make docs-check`, `make secret-scan`, `make verify`, `make test`, `make
  race`, `make invariants`, `make validate-invariants`, and structure checks.
- Run a hostile audit for role terminology, stale paths, raw prompt leakage,
  unsafe examples, provider-dependent diagnostics, and claims not enforced at
  dispatch.
- Allocate invariant IDs only at implementation landing and produce the
  required `mivia-report/v1` record.

## Exit criteria

No active plan or owned document may imply a role collection, a mutable global
agent, a selectable compiled fallback, or a privilege field without a real
enforcement point.
