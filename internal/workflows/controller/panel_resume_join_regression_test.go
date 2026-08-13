package controller

import (
	"context"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestJoinInFlightPanelAttemptLeavesItForAdvance: resume must not hard-fail on
// an in-flight agent_panel attempt. Panel steps never carry a StepRuntime
// (members run through the PanelCoordinator and are re-joined by
// advancePanelStep's D14 findResumablePanelAttempt under the run claim), so a
// missing runtime is the NORMAL state for a panel step, not a crash artifact:
// JoinInFlightAttempt leaves the attempt in-flight and Advance re-drives it.
//
// Regression: resume aborted with "step %q has no snapshotted runtime", which
// parked any run that died mid-panel-attempt forever (the CLI resume join could
// not finish, so the run never reached Advance's reconciliation; cancel was
// blocked on the orphaned member task; delete refused the active status).
func TestJoinInFlightPanelAttemptLeavesItForAdvance(t *testing.T) {
	ctx := context.Background()
	ctrl, repo, _, step := panelStepFixture(t, "wfr-panel-join")
	if err := ctrl.Start(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the crash artifact: a RUNNING panel attempt with recorded member
	// executions, left behind by the harness that died mid-attempt. Panel
	// attempts persist their member runs in PanelExecution (D14 resumes from
	// exactly these), so the artifact is realistic, not synthetic.
	attempt, err := ctrl.buildPanelAttempt(ctx, run, step, nil)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Status = workflowledger.AttemptStatusRunning
	if err := repo.CreateStepAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	// Regression: this used to error "step \"review\" has no snapshotted runtime".
	if err := ctrl.JoinInFlightAttempt(ctx, attempt); err != nil {
		t.Fatalf("JoinInFlightAttempt() error = %v, want nil (leave the panel attempt in-flight)", err)
	}
	got, done, err := ctrl.Advance(ctx)
	// The panel re-drives and settles deterministically. With the fixture's
	// stub member handler the member report fails the strict panel-review
	// decode, so Advance settles the run FAILED and reports the cause — the
	// assertion that matters is that the run reached a terminal state instead
	// of staying stranded in-flight (the pre-fix behavior errored out of the
	// resume join with "no snapshotted runtime" before Advance ever ran).
	if err != nil && !(done && workflowledger.IsTerminalRunStatus(got.Status)) {
		t.Fatalf("Advance() error = %v, want the panel attempt re-driven to a terminal settle", err)
	}
	if !done || !workflowledger.IsTerminalRunStatus(got.Status) {
		t.Fatalf("advance = %+v, done=%t; want a terminal run, not a stranded in-flight panel attempt", got, done)
	}
	attempts, err := repo.ListStepAttempts(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.AttemptID == attempt.AttemptID && a.Status == workflowledger.AttemptStatusRunning {
			t.Fatalf("attempts = %+v, want the seeded attempt no longer running", attempts)
		}
	}
}
