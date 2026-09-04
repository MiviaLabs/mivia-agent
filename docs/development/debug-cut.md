# Fast debug path cut (2026-08-17)

Status: temporary. This page is the registry and restore procedure for the
cut; delete it (and its `debug-cut` topic in `docs/OWNERS.yaml`) when the cut
is reverted. **Not being restored yet** - this page is being kept current
(paths, coverage gaps) while the cut stays in place; see "Open cleanup items"
below for what remains before a restore is even attempted.

## What is cut

To keep the dev loop fast on the debug path, several review gates are
commented out of the shipped workflow TOMLs (they are not deleted, so the
files stay diff-friendly for the revert):

- `.mivia/workflows/bug-fix.toml`: `review_panel` (agent_panel), `review`,
  and `perf_verify` are commented out; implement and the repair steps route
  straight to `test_validate`.
- `.mivia/workflows/bug-fix-fast.toml`: the same three steps
  (`review_panel`, `review`, `perf_verify`) are commented out, in the same
  shape, for the same reason. This file was not in the original registry
  entry (it did not exist on 2026-08-17; it was added 2026-08-18, already
  carrying the cut - see `c0a28dc5`). It has **no contract test** pinning
  either its cut or restored shape (unlike `bug-fix.toml`, which has
  `assertBugFixFastPathShape` in `bug_fix_panel_contract_test.go`). Adding
  that pin is an open cleanup item, independent of the eventual restore.
- `.mivia/workflows/feature-delivery.toml`: `plan_review`, `test_plan_review`,
  and `review_integration` are commented out; `review_panel` is the only
  remaining reviewer.

The shipped `bug-fix.toml` digest pin in
`internal/cliworkflow/workflows_step_defaults_integration_test.go`
(`TestStepDefaultsDigestSafeForExistingFiles`, `pinnedPreFeatureDigest`) was
re-captured on 2026-08-18 for the cut (see the pin's comment, which also
names the cut). Current pinned value:
`13283539a02b4cdd7d4f614d857e9f35417780731455f79e2febbb9873bb9c9b`
(verified current against the checked-in file as of 2026-09-04 - the test
still passes). Revert that pin when the cut is reverted.

## Package layout moved (2026-08-22) - paths below are current

The original 2026-08-17 registry entry pointed every disabled test at
`internal/cli/...`. On 2026-08-22 these files moved (`git log --follow
--diff-filter=R`, renames R098/R099) into their current homes:

- `internal/cli/workflows_step_defaults_integration_test.go` ->
  `internal/cliworkflow/workflows_step_defaults_integration_test.go`
- `internal/cli/bug_fix_panel_contract_test.go` ->
  `internal/clichat/bug_fix_panel_contract_test.go`
- `internal/cli/stack_chunk_scope_templates_test.go` ->
  `internal/clichat/stack_chunk_scope_templates_test.go`
- `internal/cli/feature_delivery_contract_test.go` ->
  `internal/clichat/feature_delivery_contract_test.go`
- `internal/cli/feature_delivery_panel_contract_test.go` ->
  `internal/clichat/feature_delivery_panel_contract_test.go`

The `git show ce7538ad:internal/cli/...` restore commands below still work
unmodified - `ce7538ad` predates the move, so the old `internal/cli/...`
path is correct for that specific historical revision. Only the CURRENT
(post-move) location differs from the original registry, and only the
current-location edits (steps 2-3 below) need the new paths.

## Disabled tests

Contract tests that pin the cut steps cannot run while the steps are
commented out. `assertFeatureDeliveryIntegrationGate`'s call site is disabled
in place (commented out). The other three test bodies were removed instead -
commenting out a ~30-70 line test function would have exceeded the repo's
30-line comment-block gate - and are recoverable only from git history at
`ce7538ad` (see the restore procedure below). Each entry records what it
guarded and when to restore it:

| File (current path) | Disabled test | Guards | Restore when |
|---|---|---|---|
| `internal/clichat/bug_fix_panel_contract_test.go` | `TestBugFixPanelMemberTemplatesRenderWithoutRound` | Panel member templates render without a round input (`buildPanelAttempt` never injects `inputs.round`); a member template copied from an agent_gate template fails `template.Render` on every dispatch | `review_panel` returns to `bug-fix.toml` |
| `internal/clichat/bug_fix_panel_contract_test.go` | `TestBugFixPanelMembersAdmit` | Every committed panel member and the `review-synthesizer` pass `validatePanelAgentTools`, the exact admission check `workflow_run` runs before a run starts | `review_panel` returns to `bug-fix.toml` |
| `internal/clichat/stack_chunk_scope_templates_test.go` | `TestReviewIntegrationTemplateRendersChunkScope` | `review_integration` binds `inputs.chunk_plan` as `chunk_scope` and its template renders with and without it. Live finding 2026-08-15: without the binding the step graded a chunk's diff against the WHOLE task spec, raised unfixable "missing sibling packages", and `reviewMadeNoProgress` killed the run | `review_integration` returns to `feature-delivery.toml` |
| `internal/clichat/feature_delivery_contract_test.go` | `assertFeatureDeliveryIntegrationGate(t, workflow)` call (commented inline in the contract test) | The integration-gate contract pins (schemas require inspected, gate feedback channels) | `review_integration` returns to `feature-delivery.toml` |

`internal/clichat/bug_fix_panel_contract_test.go` also carries
`assertBugFixFastPathShape`, which is NOT disabled - it actively pins the
*cut* shape (implement and every repair step routing straight to
`test_validate`, and `review_panel`/`review`/`perf_verify` all absent) for
`bug-fix.toml`. There is no equivalent pin for `bug-fix-fast.toml`.

## Open cleanup items (before any restore is attempted)

These do not touch the workflow TOMLs and are safe to do while the cut
stays in place:

1. **Add a fast-path shape pin for `bug-fix-fast.toml`.** Mirror
   `assertBugFixFastPathShape` for the fast variant so an accidental edit
   to `bug-fix-fast.toml`'s cut shape (e.g. a stray uncommented step, or a
   transition drifting from the `bug-fix.toml` twin) fails a test instead
   of only surfacing at run time.
2. **Verify the `bug-fix.toml` digest pin is still exact.** Confirmed
   current as of 2026-09-04 (`TestStepDefaultsDigestSafeForExistingFiles`
   passes with the value recorded above). Re-verify after any edit to
   `bug-fix.toml`, cut-related or not.
3. **Keep this registry's paths current** if `internal/clichat` or
   `internal/cliworkflow` move again. The 2026-08-22 split from
   `internal/cli` is the only move so far; check with `git log --follow`
   on each listed file before trusting the paths above.

## Restore procedure (do not run yet - registry only)

1. Un-comment the steps in the three workflow TOMLs (`bug-fix.toml`,
   `bug-fix-fast.toml`, `feature-delivery.toml`) and rewire the affected
   transitions (bug-fix / bug-fix-fast: implement + repair steps back
   through `review_panel`/`review`/`perf_verify`; feature-delivery: re-add
   `plan_review`/`test_plan_review`/`review_integration`).
2. Re-enable the disabled tests (current paths, see above). The live
   bodies are in git history at `ce7538ad`, the last commit before the
   cut - not `HEAD`, which moves; the OLD path is correct for this
   specific historical revision:
   - `git show ce7538ad:internal/cli/bug_fix_panel_contract_test.go`
   - `git show ce7538ad:internal/cli/stack_chunk_scope_templates_test.go`
   - re-add the `assertFeatureDeliveryIntegrationGate(t, workflow)` call in
     `internal/clichat/feature_delivery_contract_test.go` (current path)
   - write the new `bug-fix-fast.toml` equivalent of these tests; none
     existed pre-cut because the file did not exist yet (open cleanup item
     1 above is a prerequisite: land that pin first, adapt it here)
3. Re-capture the `bug-fix.toml` digest pin
   (`pinnedPreFeatureDigest` in
   `internal/cliworkflow/workflows_step_defaults_integration_test.go`,
   current path) - it will change the moment the TOML is edited.
4. Run the structure gate (`make gate` or the pre-commit hook) so the
   re-enabled comment blocks stay within the 30-line comment-block limit.
5. Delete this page and its `debug-cut` topic from `docs/OWNERS.yaml`.
