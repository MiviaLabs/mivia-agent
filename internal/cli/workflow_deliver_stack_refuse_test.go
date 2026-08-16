package cli

// Pins two live e2e findings:
//
//  1. (2026-08-15) `mivia workflow run`/`resume` drive a multi-chunk stacking
//     plan run's stack BEFORE delivering the plan run itself (see
//     maybeDriveSettledStack's call site comments: "drive-before-delivery
//     ordering"). `mivia workflow deliver <planRunID> --allow-publish`, used
//     directly (the natural recovery command for a stuck/killed stack),
//     skipped that ordering entirely: it settled the plan run's own delivery
//     (which, for a stacking workflow with deliver_plan_run=false, has no
//     diff and settles succeeded) without ever driving the stack. `workflow
//     deliver` must refuse an INCOMPLETE multi-chunk plan run and redirect
//     the operator to the commands that drive it.
//  2. (F11, 2026-08-16) the refusal above was unconditional: it fired on
//     EVERY multi-chunk plan run, including one whose stack had already
//     driven to completion (every chunk merged, integration run settled) -
//     and no other CLI command (`resume`, `cancel`) could settle it either,
//     parking it at delivery_pending forever. `workflow deliver` must settle
//     (or, for deliver_plan_run=true, actually deliver) a COMPLETE stack's
//     plan run instead of refusing it too.

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestClassifyStackPlanRunDeliveryIncompleteForUndrivenMultiChunkPlan(t *testing.T) {
	root, _, store, repo, compiled := newWorkflowBuildFixture(t)
	ctx := context.Background()
	runID := "wfr-plan-undriven"
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	seedSucceededDecomposeAttempt(t, repo, runID, []byte(multiChunkPlanOutput))

	if got := classifyStackPlanRunDelivery(ctx, root, store, repo, runID, true); got != stackPlanRunIncomplete {
		t.Fatalf("classifyStackPlanRunDelivery() = %v, want stackPlanRunIncomplete for an undriven multi-chunk plan", got)
	}
	err := errUndrivenStackPlanRun(runID)
	if !strings.Contains(err.Error(), "mivia stack drive") {
		t.Fatalf("refusal %q must point at `mivia stack drive` - the one command that drives a parked stack", err)
	}
	// `mivia workflow resume` refuses delivery_pending runs and
	// `mivia workflow run` mints a NEW plan run (a second stack) instead of
	// driving the parked one, so the refusal must not send the operator to
	// either dead end.
	if strings.Contains(err.Error(), "mivia workflow run") || strings.Contains(err.Error(), "mivia workflow resume") {
		t.Fatalf("refusal %q must not advise `workflow run`/`workflow resume`: neither can drive a parked plan run", err)
	}
}

// TestClassifyStackPlanRunDeliveryNotApplicableForEverythingElse: a run with
// no succeeded decompose attempt (a chunk run, an integration run, a
// non-stacking run, or a still-in-progress plan run) must never be gated -
// the check only exists to catch a multi-chunk plan run delivered directly.
func TestClassifyStackPlanRunDeliveryNotApplicableForEverythingElse(t *testing.T) {
	root, _, store, repo, compiled := newWorkflowBuildFixture(t)
	ctx := context.Background()
	if got := classifyStackPlanRunDelivery(ctx, root, store, repo, "wfr-does-not-exist", true); got != stackPlanRunNotApplicable {
		t.Fatalf("classifyStackPlanRunDelivery() on an unknown run = %v, want stackPlanRunNotApplicable (best-effort, never blocks)", got)
	}
	runID := "wfr-chunk-c1"
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	// No decompose attempt at all (chunk runs never run decompose).
	if got := classifyStackPlanRunDelivery(ctx, root, store, repo, runID, true); got != stackPlanRunNotApplicable {
		t.Fatalf("classifyStackPlanRunDelivery() on a chunk run = %v, want stackPlanRunNotApplicable", got)
	}
}

// TestClassifyStackPlanRunDeliveryCompleteForDrivenStack pins F11: a
// multi-chunk plan run whose stack already drove to completion (every chunk
// merged, integration run settled) under a non-auto merge policy must be
// classified complete, not incomplete - `workflow deliver` settles it
// instead of refusing it forever.
func TestClassifyStackPlanRunDeliveryCompleteForDrivenStack(t *testing.T) {
	ctx := context.Background()
	root, store, repo, stackID := seedUnmergedIntegrationStack(t)

	if got := classifyStackPlanRunDelivery(ctx, root, store, repo, stackID, true); got != stackPlanRunComplete {
		t.Fatalf("classifyStackPlanRunDelivery() = %v, want stackPlanRunComplete for a driven approve-policy stack", got)
	}
}
