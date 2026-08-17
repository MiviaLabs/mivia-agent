# Fast debug path cut (2026-08-17)

Status: temporary. This page is the registry and restore procedure for the
cut; delete it (and its `debug-cut` topic in `docs/OWNERS.yaml`) when the cut
is reverted.

## What is cut

To keep the dev loop fast on the debug path, several review gates are
commented out of the shipped workflow TOMLs (they are not deleted, so the
files stay diff-friendly for the revert):

- `.mivia/workflows/bug-fix.toml`: `review_panel` (agent_panel), `review`,
  and `perf_verify` are commented out; implement and the repair steps route
  straight to `test_validate`.
- `.mivia/workflows/feature-delivery.toml`: `plan_review`, `test_plan_review`,
  and `review_integration` are commented out; `review_panel` is the only
  remaining reviewer.

The shipped `bug-fix.toml` digest pin in
`internal/cli/workflows_step_defaults_integration_test.go` was re-captured on
2026-08-17 for the cut (see the pin's comment, which also names the cut).
Revert that pin when the cut is reverted.

## Disabled tests

Contract tests that pin the cut steps cannot run while the steps are
commented out. They are disabled in place (commented out) rather than
deleted, so the regression guards survive the cut. Each entry records what it
guarded and when to restore it:

| File | Disabled test | Guards | Restore when |
|---|---|---|---|
| `internal/cli/bug_fix_panel_contract_test.go` | `TestBugFixPanelMemberTemplatesRenderWithoutRound` | Panel member templates render without a round input (`buildPanelAttempt` never injects `inputs.round`); a member template copied from an agent_gate template fails `template.Render` on every dispatch | `review_panel` returns to `bug-fix.toml` |
| `internal/cli/bug_fix_panel_contract_test.go` | `TestBugFixPanelMembersAdmit` | Every committed panel member and the `review-synthesizer` pass `validatePanelAgentTools`, the exact admission check `workflow_run` runs before a run starts | `review_panel` returns to `bug-fix.toml` |
| `internal/cli/stack_chunk_scope_templates_test.go` | `TestReviewIntegrationTemplateRendersChunkScope` | `review_integration` binds `inputs.chunk_plan` as `chunk_scope` and its template renders with and without it. Live finding 2026-08-15: without the binding the step graded a chunk's diff against the WHOLE task spec, raised unfixable "missing sibling packages", and `reviewMadeNoProgress` killed the run | `review_integration` returns to `feature-delivery.toml` |
| `internal/cli/feature_delivery_contract_test.go` | `assertFeatureDeliveryIntegrationGate(t, workflow)` call (commented inline in the contract test) | The integration-gate contract pins (schemas require inspected, gate feedback channels) | `review_integration` returns to `feature-delivery.toml` |

## Restore procedure

1. Un-comment the steps in the two workflow TOMLs and rewire the affected
   transitions (bug-fix: implement + repair steps back through
   `review_panel`/`review`/`perf_verify`; feature-delivery: re-add
   `plan_review`/`test_plan_review`/`review_integration`).
2. Re-enable the disabled tests. The live bodies are in git history, exactly
   as committed before the cut:
   - `git show HEAD:internal/cli/bug_fix_panel_contract_test.go`
   - `git show HEAD:internal/cli/stack_chunk_scope_templates_test.go`
   - re-add the `assertFeatureDeliveryIntegrationGate(t, workflow)` call in
     `internal/cli/feature_delivery_contract_test.go`
3. Re-capture the `bug-fix.toml` digest pin in
   `internal/cli/workflows_step_defaults_integration_test.go` (it will change
   the moment the TOML is edited).
4. Run the structure gate (`make gate` or the pre-commit hook) so the
   re-enabled comment blocks stay within the 30-line comment-block limit.
5. Delete this page and its `debug-cut` topic from `docs/OWNERS.yaml`.
