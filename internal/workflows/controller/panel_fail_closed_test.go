package controller

import (
	"context"
	"strings"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestPanelStepRefusalPersistsDurableCause: the fail-closed refusal must leave
// a durable attempt row. The attempt settles failed with a non-empty ErrorRef
// that resolves to the refusal cause, and the run reaches a terminal status
// (G9 fail-closed, no hang). The ProgressPanelRefused event carries the cause.
func TestPanelStepRefusalPersistsDurableCause(t *testing.T) {
	ctrl, repo, _, step := panelStepFixture(t, "wfr-panel-refused")
	sink := &recordingProgressSink{}
	if err := ctrl.SetProgressSink(sink); err != nil {
		t.Fatal(err)
	}
	run, err := ctrl.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Wave 5 synthesis unavailable") {
		t.Fatalf("Run error = %v, want refusal cause", err)
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
		t.Fatal("attempt ErrorRef is empty, want the refusal cause")
	}
	body, err := repo.LoadContent(context.Background(), attempt.ErrorRef)
	if err != nil {
		t.Fatalf("load ErrorRef content: %v", err)
	}
	want := `agent_panel step "review" is not supported (Wave 5 synthesis unavailable)`
	if string(body) != want {
		t.Fatalf("ErrorRef content = %q, want %q", body, want)
	}
	events := sink.take()
	for _, e := range events {
		if e.Kind == ProgressPanelRefused && e.StepID == step.ID && e.AttemptNo == attempt.AttemptNo && e.Detail == want {
			return
		}
	}
	t.Fatalf("no ProgressPanelRefused event with cause among %+v", events)
}
