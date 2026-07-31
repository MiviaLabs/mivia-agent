# 06.04 — Verification and closeout

**Goal:** Audit the complete binding against trust, lifecycle, concurrency, and repository gates.
**Depends on:** [03](03-runtime-enforcement.md).

## Checks

- Challenge the root-only boundary and prove no nested or legacy handler path
  can invoke a disallowed skill.
- Test workspace shadowing, malformed definitions, renamed files, model switch,
  resume, retry, idempotency, cancellation, and concurrent instances.
- Reconcile plans `05`, `07`, `08`, `09`, `.mivia/INDEX.md`, and the invariant
  manifest; allocate a new invariant ID only at implementation landing.
- Run `go test ./...`, `go test -race ./...`, `make verify`, `make invariants`,
  `make validate-invariants`, `make structure-check`, and `make docs-check`.
- Complete a hostile bug-audit round and produce the required `mivia-report/v1`.

## Exit criteria

No claim of skill isolation is accepted unless a disallowed skill is rejected at
the actual task boundary, metadata makes the tool subset non-vacuous, and the
same result holds for concurrent instances and resumed work.
