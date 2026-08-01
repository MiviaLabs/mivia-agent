# 07.3 - Agent routing verification and closeout

**Goal:** Prove concurrent named-agent instances, strict task selection, and
complete the routing control surface.
**Depends on:** phases `01` and `02`.

## Verification

- Fan out many instances from one immutable definition under `go test -race`.
- Verify missing, unknown, renamed, and unauthorized `agent` values fail before
  task or ledger creation and list only sanitized available names.
- Verify `handler`, `name`, `role`, built-in runner names, and unknown nested
  fields are rejected by both schemas and direct JSON execution; prove there is
  no implicit runner selection.
- Verify explicit skill selection is checked against the selected task agent,
  cannot widen the caller's authority, and remains isolated across concurrent
  instances and model-switch generations.
- Cover cancellation, per-instance turn budgets, model-switch generations,
  stale-definition rejection, resume re-authorization, idempotency replay, and
  agent/skill/runtime-handler/tool namespace collisions.
- Run `go test ./internal/cli/... ./internal/subagents/... ./internal/coordinator/... -race`.
- Run `make verify`, `make invariants`, and `make validate-invariants`.
- Update `.mivia/INDEX.md`, plan `00`, plan `08`, and any invariant pointer in
  the same implementation change. Reconcile the shipped skill-policy wording
  with the explicit `skill` field; do not leave a second selector contract in
  an active pointer or model-facing prompt.

## Mutation proofs

The implementation must fail tests when it:

- accepts a task without `agent`;
- translates `handler`, `name`, or a built-in runner into an agent;
- omits agent identity or the effective definition digest from the fingerprint;
- resumes without rechecking current agent and skill access;
- lets a selected task agent widen the caller's dispatch boundary;
- shares mutable definition or registry state across instances; or
- authorizes a skill from the root scope instead of the selected task agent's
  immutable policy.

## Closeout gate

The plan is complete only when the focused tests, race tests, repository gates,
and the hostile bug-audit loop report no remaining routing or authority defect,
and the final `mivia-report/v1` records the exact evidence.
