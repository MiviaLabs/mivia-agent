package controller

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestPanelStepFailureCausePersistsDurableCause: a panel step that fails
// closed (an invalid member report, see
// TestPanelStepFailsClosedOnInvalidMemberReport) must leave a durable attempt
// row. The attempt settles failed with a non-empty ErrorRef that resolves to
// the failure cause, and the run reaches a terminal status (G9 fail-closed,
// no hang). A run_failed progress event carries the same cause.
func TestPanelStepFailureCausePersistsDurableCause(t *testing.T) {
	ctrl, repo, _, step := panelStepFixture(t, "wfr-panel-refused")
	sink := &recordingProgressSink{}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	run, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "panel member") {
		t.Fatalf("Run error = %v, want a panel member report failure", err)
	}
	if !workflowledger.IsTerminalRunStatus(run.Status) {
		t.Fatalf("Run status = %q, want terminal", run.Status)
	}
	attempts, err := repo.ListStepAttempts(context.Background(), ctrl.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var attempt workflowledger.StepAttempt
	for _, a := range attempts {
		if a.StepID == step.ID {
			attempt = a
			break
		}
	}
	if attempt.AttemptID == "" {
		t.Fatal("no panel attempt recorded")
	}
	if attempt.Status != workflowledger.AttemptStatusFailed {
		t.Fatalf("attempt status = %q, want failed", attempt.Status)
	}
	if attempt.ErrorRef == "" {
		t.Fatal("attempt ErrorRef is empty, want the failure cause")
	}
	body, err := repo.LoadContent(context.Background(), attempt.ErrorRef)
	if err != nil {
		t.Fatalf("load ErrorRef content: %v", err)
	}
	if !strings.Contains(string(body), "panel member") {
		t.Fatalf("ErrorRef content = %q, want it to name the failing panel member", body)
	}
	events := sink.take()
	for _, e := range events {
		if e.Kind == ProgressRunFailed && e.Detail == string(body) {
			return
		}
	}
	t.Fatalf("no ProgressRunFailed event carrying the failure cause among %+v", events)
}
