package cli

// Pins a live e2e finding (2026-08-15): `mivia workflow run`/`resume` drive a
// multi-chunk stacking plan run's stack BEFORE delivering the plan run
// itself (see maybeDriveSettledStack's call site comments: "drive-before-
// delivery ordering"). `mivia workflow deliver <planRunID> --allow-publish`,
// used directly (the natural recovery command for a stuck/killed stack),
// skips that ordering entirely: it settles the plan run's own delivery
// (which, for a stacking workflow with deliver_plan_run=false, has no diff
// and settles succeeded) without ever driving the stack. The chunks the
// plan decomposed - and the final integration run - are silently abandoned:
// the plan run reports "succeeded" while the actual work never finished.
// `workflow deliver` must refuse a multi-chunk plan run and redirect the
// operator to the commands that drive it, instead of corrupting the run's
// terminal status.

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestRefuseUndrivenStackPlanRunRejectsAMultiChunkPlan(t *testing.T) {
	_, _, _, repo, _ := newWorkflowBuildFixture(t)
	compiled := compileFeatureDeliveryWorkflow(t)
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

	err := refuseUndrivenStackPlanRun(ctx, repo, runID)
	if err == nil {
		t.Fatal("refuseUndrivenStackPlanRun succeeded on a multi-chunk plan run, want a refusal")
	}
	if !strings.Contains(err.Error(), "stack") || !strings.Contains(err.Error(), "workflow run") {
		t.Fatalf("refusal %q must mention the stack and point at the drive commands", err)
	}
}

// TestRefuseUndrivenStackPlanRunAllowsEverythingElse: a run with no succeeded
// decompose attempt (a chunk run, an integration run, a non-stacking run, or
// a still-in-progress plan run) must never be refused - the check only
// exists to catch a SETTLED multi-chunk plan run delivered directly.
func TestRefuseUndrivenStackPlanRunAllowsEverythingElse(t *testing.T) {
	_, _, _, repo, _ := newWorkflowBuildFixture(t)
	ctx := context.Background()
	if err := refuseUndrivenStackPlanRun(ctx, repo, "wfr-does-not-exist"); err != nil {
		t.Fatalf("refuseUndrivenStackPlanRun on an unknown run: %v, want nil (best-effort, never blocks)", err)
	}
	compiled := compileFeatureDeliveryWorkflow(t)
	runID := "wfr-chunk-c1"
	snap := workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: compiled.Name, WorkflowDigest: compiled.Digest,
		Status: workflowledger.RunStatusPending,
	}
	if err := repo.CreateRun(ctx, snap, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	// No decompose attempt at all (chunk runs never run decompose).
	if err := refuseUndrivenStackPlanRun(ctx, repo, runID); err != nil {
		t.Fatalf("refuseUndrivenStackPlanRun on a chunk run: %v, want nil", err)
	}
}
