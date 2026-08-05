package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func TestLinearControllerDoesNotRetryTerminalAttempt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		attempt workflowledger.AttemptStatus
		wantRun workflowledger.RunStatus
		wantErr error
	}{
		{name: "failed", attempt: workflowledger.AttemptStatusFailed, wantRun: workflowledger.RunStatusFailed},
		{name: "canceled", attempt: workflowledger.AttemptStatusCanceled, wantRun: workflowledger.RunStatusCanceled, wantErr: context.Canceled},
		{name: "timed out", attempt: workflowledger.AttemptStatusTimedOut, wantRun: workflowledger.RunStatusTimedOut, wantErr: context.DeadlineExceeded},
		{name: "succeeded without route", attempt: workflowledger.AttemptStatusSucceeded, wantRun: workflowledger.RunStatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf := linearWorkflow(t)
			repo := workflowledger.NewMemoryRepository()
			runner := &linearRunner{}
			ctrl, err := NewLinearController(repo, runner, wf, map[string]StepRuntime{"first": {Agent: agents.ResolvedAgent{Name: "one"}}}, map[string]any{"task": "build"}, "wfr-terminal-"+strings.ReplaceAll(tc.name, " ", "-"), []byte("snapshot"))
			if err != nil {
				t.Fatal(err)
			}
			if err := ctrl.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			run, _ := repo.GetRun(context.Background(), ctrl.RunID)
			if err := repo.CompareAndSetRunStatus(context.Background(), ctrl.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != nil {
				t.Fatal(err)
			}
			attempt := workflowledger.StepAttempt{AttemptID: "wfa-first-1", RunID: ctrl.RunID, StepID: "first", AttemptNo: 1, Status: workflowledger.AttemptStatusRunning}
			if err := repo.CreateStepAttempt(context.Background(), attempt); err != nil {
				t.Fatal(err)
			}
			stored, _ := repo.GetStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID)
			if err := repo.CompleteStepAttempt(context.Background(), ctrl.RunID, attempt.AttemptID, stored.Version, workflowledger.AttemptOutcome{Status: tc.attempt}); err != nil {
				t.Fatal(err)
			}
			got, _, err := ctrl.Advance(context.Background())
			if got.Status != tc.wantRun {
				t.Fatalf("status = %q, want %q; err=%v", got.Status, tc.wantRun, err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			attempts, _ := repo.ListStepAttempts(context.Background(), ctrl.RunID)
			if len(attempts) != 1 || len(runner.calls) != 0 {
				t.Fatalf("attempts=%d calls=%d", len(attempts), len(runner.calls))
			}
		})
	}
}
