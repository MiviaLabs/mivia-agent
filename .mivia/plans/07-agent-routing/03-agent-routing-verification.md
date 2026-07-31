# 07.3 — Agent routing verification and closeout

**Goal:** Prove concurrent agent instances and complete the routing control surface.
**Depends on:** phases `01` and `02`.

## Verification

- Fan out many instances from one definition under `go test -race`.
- Cover cancellation, per-instance turn budgets, model-switch generations,
  stale-definition rejection, and handler/agent/skill namespace collisions.
- Run `go test ./internal/cli/... ./internal/subagents/... ./internal/coordinator/... -race`.
- Run `make verify`, `make invariants`, and `make validate-invariants`.
- Update `.mivia/INDEX.md`, plan `00`, plan `08`, and any invariant pointer in
  the same change; do not leave old role terminology as a second contract.

## Mutation proofs

The implementation must fail tests when it ignores `agent`, omits agent identity
from the fingerprint, resumes without rechecking access, or shares mutable
definition/registry state across instances.
