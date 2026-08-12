package controller

import (
	"context"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Durability and ownership matrix items 2-3: resume joins each existing
// member run from its exact persisted PanelExecution and never rebuilds (and
// so never redispatches) an attempt that is still non-terminal.
// findResumablePanelAttempt is the exact mechanism advancePanelStep uses at
// every Advance entry (fresh dispatch and resume alike) to decide this.
func TestFindResumablePanelAttempt_JoinsExistingNonTerminalAttempt(t *testing.T) {
	nonTerminal := workflowledger.StepAttempt{
		AttemptID: "attempt-1", StepID: "review", Status: workflowledger.AttemptStatusRunning,
		PanelExecution: &workflowledger.PanelExecution{Phase: workflowledger.PanelPhaseMembersAdmitted},
	}
	attempts := []workflowledger.StepAttempt{nonTerminal}

	found, ok := findResumablePanelAttempt(attempts, "review")
	if !ok {
		t.Fatal("expected the existing non-terminal panel attempt to be found for resume")
	}
	if found.AttemptID != nonTerminal.AttemptID {
		t.Fatalf("found attempt %q, want %q (must join the SAME attempt, never mint a new one)", found.AttemptID, nonTerminal.AttemptID)
	}
}

// Item 3: a terminal (already-completed) attempt is never rejoined — the
// caller must build a fresh one for the step's next attempt instead of
// resuming a settled member's dead work.
func TestFindResumablePanelAttempt_IgnoresTerminalAttempt(t *testing.T) {
	terminal := workflowledger.StepAttempt{
		AttemptID: "attempt-1", StepID: "review", Status: workflowledger.AttemptStatusSucceeded,
		PanelExecution: &workflowledger.PanelExecution{Phase: workflowledger.PanelPhaseSynthesisAdmitted},
	}
	if _, ok := findResumablePanelAttempt([]workflowledger.StepAttempt{terminal}, "review"); ok {
		t.Fatal("a terminal panel attempt must never be treated as resumable")
	}
}

// A non-panel attempt (PanelExecution == nil) for the same step must never
// be picked up by the panel resume path.
func TestFindResumablePanelAttempt_IgnoresNonPanelAttempt(t *testing.T) {
	nonPanel := workflowledger.StepAttempt{AttemptID: "attempt-1", StepID: "review", Status: workflowledger.AttemptStatusRunning}
	if _, ok := findResumablePanelAttempt([]workflowledger.StepAttempt{nonPanel}, "review"); ok {
		t.Fatal("an attempt with no PanelExecution must never be treated as a resumable panel attempt")
	}
}

// End-to-end: driving the panel synthesis pipeline through
// driveAdvancePanelSynthesis a first time, then confirming a fresh
// buildPanelAttempt call against the resulting terminal attempt list mints
// the NEXT attempt number rather than colliding with the first attempt's
// deterministic child IDs (buildPanelAttempt derives PanelChildIDs from the
// attempt ID, so two attempts for the same step always get distinct
// coordinator children).
func TestAdvancePanelStep_NewAttemptAfterTerminalNeverCollidesWithPriorChildren(t *testing.T) {
	ctrl, repo, step := panelSynthesisFixture(t, "wfr-resume-join", `{"verdict":"approved","findings":[]}`, `{"dispositions":[],"summary":"ok"}`)
	firstRun, firstDone, firstErr := driveAdvancePanelSynthesis(t, ctrl, repo, step)
	if firstErr != nil {
		t.Fatalf("first pass error = %v", firstErr)
	}
	if !firstDone || firstRun.Status != workflowledger.RunStatusSucceeded {
		t.Fatalf("first pass run = %+v done = %v, want a succeeded terminal run", firstRun, firstDone)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != workflowledger.AttemptStatusSucceeded {
		t.Fatalf("attempts = %+v, want exactly one succeeded attempt", attempts)
	}
	firstAttempt := attempts[0]
	if _, ok := findResumablePanelAttempt(attempts, step.ID); ok {
		t.Fatal("a terminal attempt must not be reported resumable")
	}

	ctx := workflowledger.ContextWithClaimHolder(context.Background(), ctrl.Holder)
	if err := repo.ClaimRun(ctx, ctrl.RunID, ctrl.Holder); err != nil {
		t.Fatal(err)
	}
	run, err := repo.GetRun(ctx, ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := ctrl.buildPanelAttempt(ctx, run, step, attempts)
	if err != nil {
		t.Fatalf("buildPanelAttempt() error = %v", err)
	}
	if rebuilt.AttemptNo == firstAttempt.AttemptNo {
		t.Fatal("a fresh attempt after a terminal one must mint the next attempt number, not reuse it")
	}
	for i, member := range rebuilt.PanelExecution.Members {
		if member.CoordinatorRunID == firstAttempt.PanelExecution.Members[i].CoordinatorRunID {
			t.Fatalf("member %q: new attempt's child run ID collides with the first attempt's (%s)", member.MemberID, member.CoordinatorRunID)
		}
	}
}
