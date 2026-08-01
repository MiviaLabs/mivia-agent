# 09.03 - Verification and closeout

**Goal:** Close the agent documentation program only after owned documents,
fixtures, and accepted implementation contracts agree.
**Depends on:** [02](02-parser-backed-examples.md).

## Checks

- Re-read the accepted plan-08 implementation/tests before documenting command
  grammar, diagnostics, or identity fields. If 08 differs from the locked
  contract, return to plan 09 Step 0 instead of documenting a forecast.
- Reconcile only `.mivia/INDEX.md`, `.mivia/plans/00-agent-program-overview.md`,
  and active plans `08`/`09` with the owned documents. Preserve archived plans
  as historical records; do not rewrite old designs merely for terminology.
- Audit the document matrix for stale role terminology, obsolete paths,
  workspace-gate overclaims, mutable/global-agent claims, root-chat-resume
  claims, raw prompt leakage, provider/model-policy invention, unsafe tool
  examples, and commands missing from the accepted CLI surface.
- Run the fixture tests, focused docs/link inspection, `make docs-check`, and
  `make secret-scan`, then `make verify`, `make test`, `make race`, and `make
  invariants`. `make verify` already covers invariant-reference validation,
  structure, Semgrep, hooks, docs, secrets, Go tests/vet/build; do not list its
  components as independently passed unless separately run.
- Complete the hostile documentation/security audit and produce
  `mivia-report/v1`. Do not allocate an invariant ID: this plan adds no new
  runtime invariant.

## Exit criteria

Every edited document has an owner, all examples resolve only in their isolated
test layout, every public claim maps to shipped behavior, and no active
control-surface pointer reintroduces gated workspace-agent discovery or an
unimplemented identity/model authority claim.
