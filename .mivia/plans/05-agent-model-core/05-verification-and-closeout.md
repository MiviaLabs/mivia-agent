# 05.5 — Cross-surface verification and closeout

**Status:** DESIGN — runs only after phases `01`–`04` pass.
**Goal:** Prove the agent-definition collection is safe, discoverable, and consistent across the control surface.
**Parent:** [`00-overview.md`](00-overview.md).

## Scope

This phase owns cross-cutting verification and durable pointer updates. It does
not defer boundary tests or write the first production tests; those belong to
their owning phases under the ADLC RED→GREEN sequence.

Update only after the implementation phases have supplied their named tests:

- `Makefile` — ensure `make invariants` selects every test named by the new
  invariant row;
- `.mivia/invariants.md` — allocate the lowest free `INV-AG` ID at landing,
  then add the workspace trust invariant and all named tests atomically;
- `.mivia/INDEX.md` — point at this directory and report the blocked/design
  status accurately;
- `.mivia/plans/00-agent-program-overview.md` — link to
  `05-agent-model-core/00-overview.md`;
- active dependent plans `06`, `07`, `08`, `09`, and `42` — remove stale
  assumptions about `INV-AG-28`, markdown definitions, and the retired P2 parser;
- amend any plan that still describes a conflicting ownership boundary.

Historical archived plans may retain their original records; do not rewrite
history merely to make old prose match the current design.

## Verification ladder

Run and record actual results, without claiming any unrun check:

1. focused package tests for each phase;
2. `make verify`;
3. `make test`;
4. `make race`;
5. `make invariants`;
6. `make validate-invariants`;
7. `make structure-check`;
8. `make secret-scan`;
9. `make docs-check`.

Run the hostile bug-audit loop on the complete diff. Any confirmed security or
registry-construction defect returns to the owning phase; it is not papered over
in closeout. Produce the required `mivia-report/v1` completion record and use a
scoped conventional commit only after the diff and all applicable gates pass.

## Exit criteria

The directory is complete only when the phase map, active indexes, invariant
selection, tests, and implementation ownership agree; the security-sensitive
plan is either unblocked with evidence or remains explicitly blocked. No status
may say “design-ready” while an invariant ID, enforcement point, or dispatcher
construction path is unresolved.
