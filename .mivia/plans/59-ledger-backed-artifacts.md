# 59 — Ledger-backed evidence artifacts + failure observability

**Status**: LOCKED — ADLC Step 0 complete (challenge panel: 1 conditional PASS + 1 BLOCK; all findings dispositioned and adopted). Ready for Step 1 breakdown.

## Incident (both defects CONFIRMED, verified in the SQLite ledger)

`wfr-SQJOWF5F23QBDRSJ` died at `test_plan_review` attempt 2 in **0.6 ms** (pre-dispatch):

- **Defect 1 (size-reject)**: `plan_tests`#2 produced a 35,056-byte output (`content` ref `sha256:031c018d…`). `test_plan_review`'s binding `steps.plan_tests.output → test_plan` caps at 16,000 bytes. Four sites reject oversized values instead of degrading: `contextForStep` (`internal/workflows/controller/linear.go:358`), `validateBindingLimits` (`linear.go:449`), `template.Render` (`internal/workflows/template/render.go:52`), `marshalEvidenceSelection` (`agent_step_evidence.go:49`). The attempt failed with no output, no transition.
- **Defect 2 (silent failure)**: `failAttempt` (`linear_execution.go:373-378`) completes the attempt Failed with an **empty `AgentStepResult{}` — no `ErrorRef`** — so the cause was dropped. The `wf_attempt_completed` event carries only ids/timestamps. The same silent class exists in `settleAgentAttempt`'s route-failure path (`linear_execution.go:270-287`: a succeeded child whose route selection fails flips Failed with empty `ErrorRef`).

Step outputs already live content-addressed in the workflow ledger (SQLite `content` table, `attempt.OutputRef`) — immutable, never deleted.

## Goal

Eliminate size-induced step failures by making evidence **ledger-backed reference envelopes** (never reject; the downstream child reads the full artifact from the DB), and make **every failed attempt persist its cause** (`ErrorRef`).

## Locked boundaries

- Values **≤ the binding's own `max_bytes`** stay byte-identical inline (no behavior change for normal runs; in-flight joins are identity-keyed and unaffected).
- `template.Render` stays strict (defense-in-depth): the substitution happens at the controller chokepoint so Render only ever sees small values; the tiny-cap case (`max_bytes` < envelope size) still rejects, documented.
- **No new tool**: reuse `workflow_inspect` (already loads full attempt output from the ledger, same run/step/attempt triple, 512 KiB budget). No new `agenttools` tool.
- The read surface is **run-scoped via caller identity**: a child with `TaskIdentity` may only inspect runs whose attempts include its `CoordinatorRunID`; no-identity (root/interactive) is allowed. Pre-existing workflow tools are out of scope (not touched).
- DAG-upfront materialization (creating the whole task DAG as rows up front) is **explicitly out of scope** — a scheduler rewrite unrelated to this failure. The ledger-backed envelope is the delivery mechanism that satisfies "plans in sqlite, reviewer checks the db".
- `linear_gates.go:99` is NOT changed: its failure detail is carried by the attempt `Output`; the absence of `ErrorRef` there is deliberate.
- Defect-2 fix preserves `storeErrorText`'s fail-soft property (never masks the original cause).

## Design

1. **Envelope substitution** in `contextForStep` (`linear.go:332-371`): when a `steps.X.output` binding's marshaled value exceeds the binding's `max_bytes` (fallback `definition.MaxEvidenceBindingBytes`), substitute a compact reference envelope:
   ```json
   {"artifact":{"step":"plan_tests","attempt":2,"ref":"sha256:…","bytes":35056,"digest":"sha256:…","preview":"<first ~4KB, rune-safe, redacted, UTF-8-sanitized>"},
    "note":"full artifact is in the workflow ledger; read it with workflow_inspect(run_id=<this run>, step=<step>, attempt=<attempt>)"}
   ```
   Substitution only when the envelope fits the binding cap; otherwise keep the reject (defense-in-depth).
2. **`validateBindingLimits`** (`linear.go:419+`): measure the **evidence value** (already enveloped or inlined) against `MaxBytes` — no longer re-loads and measures the original artifact.
3. **`marshalEvidenceSelection`** (`agent_step_evidence.go`): records the envelope's bytes/digest (the artifact digest is embedded in the envelope `ref`); keep the 16 KiB aggregate metadata cap.
4. **Defect 2**: `failAttempt` passes `AgentStepResult{ErrorRef: storeErrorText(writeCtx, c.Repo, cause)}`; `settleAgentAttempt` sets `ErrorRef` when the route-selection failure flips the status to Failed.
5. **Tool grant + scoping**: add `workflow_inspect` to `.mivia/agents/reviewer.toml` and `.mivia/agents/workflow-engineer.toml`; add a caller-identity participant gate in the inspect path (`agenttools`): with `TaskIdentity`, the requested run must have an attempt whose `CoordinatorRunID == task.RunID`; unknown run / non-member → indistinguishable refusal.
6. **Templates**: all 8 evidence-inlining templates (`plan.md`, `plan-review.md`, `plan-tests.md`, `plan-tests-review.md`, `implement.md`, `review.md`, `review-integration.md`, `repair.md`) get: "evidence may be a reference envelope with a preview; when it is, read the full artifact with `workflow_inspect`".

## Files

Modify: `internal/workflows/controller/linear.go`, `internal/workflows/controller/agent_step_evidence.go`, `internal/workflows/controller/linear_execution.go`, `internal/workflows/agenttools/status.go` (+ types/service for the gate if needed), `.mivia/agents/reviewer.toml`, `.mivia/agents/workflow-engineer.toml`, 8 templates under `.mivia/workflows/templates/`.
Create: tests in `internal/workflows/controller/` (`evidence_envelope_test.go`, `error_ref_paths_test.go`), `internal/workflows/agenttools/` (`inspect_scoping_test.go`).

## Test strategy (named)

- `TestContextForStepSubstitutesEnvelopeForOversizedBinding` — 35K value vs 16K cap → evidence is an envelope (ref/bytes/preview), no error, run proceeds.
- `TestContextForStepKeepsInlineValuesByteIdentical` — small values unchanged (backward compat).
- `TestValidateBindingLimitsMeasuresEnvelope` — enveloped value passes the 16K check; tiny-cap case still rejects.
- `TestMarshalEvidenceSelectionRecordsEnvelope` — metadata bytes/digest = envelope; 16 KiB aggregate cap still enforced.
- `TestFailAttemptPersistsErrorRef` — failAttempt with a cause → attempt ErrorRef non-empty, content loadable.
- `TestSettleAgentAttemptRouteFailurePersistsErrorRef` — route-selection failure on a succeeded child → Failed attempt carries ErrorRef.
- `TestInspectScopedToCallerRun` — child identity with non-member run → refusal; own run → allowed; no identity → allowed.
- Existing pinned tests (`TestEvidenceBindingStillCapped`, `linear_changed_paths.go:231` binding-limit, `step_context_budget_test.go`) must remain green unchanged.

## Waves

1. Envelope substitution (`contextForStep` + `validateBindingLimits`) + tests. Wave gate: controller package green.
2. `marshalEvidenceSelection` metadata + Defect-2 (`failAttempt`, `settleAgentAttempt`) + tests.
3. Inspect grant + scoping gate + templates. Wave gate: full `internal/workflows/...`, `internal/cli` contract tests green.
4. ADLC Step 5 hostile audit (3 auditors) → disposition → fix.
5. Step 6: `go build ./... && go vet ./... && go test -race ./...`, TDD audit, conventional commit, push.

## Challenge disposition

- **BLOCK (correctness)**: threshold hole at `validateBindingLimits` → adopted (substitute at chokepoint, measure envelope, threshold = binding `MaxBytes`). Security: un-scoped read surface → adopted (caller-identity gate). Defect-2 scope → adopted (failAttempt + settleAgentAttempt; gates.go:99 excluded by design). Digest/preview/template-exhaustiveness → adopted.
- **PASS (structure, conditional)**: envelope right abstraction; reuse `workflow_inspect` (no new tool) → adopted; `MaxBytes=1` still rejects → confirmed, no test change; Render stays strict → adopted; DAG-upfront out of scope → documented; keep `failAttempt`+defect-2 in one plan → adopted.

## Rollback criterion

Stop and return to Step 0 if: (a) any existing run resume regresses (envelope leaks into a joined child's re-rendered prompt and changes its outcome), (b) the inspect gate breaks the root session's `workflow_inspect`, or (c) any pinned reject test must change.
