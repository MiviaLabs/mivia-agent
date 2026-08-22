package cliworkflow

// stack_failed_integration_helpers_test.go duplicates cli's stack failure
// fixtures (stack_merge_test.go, stack_git_merge_test.go,
// stack_drive_completed_test.go) for the delivery tests that live here.

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func seedIntegrationRunAdmitted(t *testing.T, repo workflowledger.Repository, stackID string, noWorktree bool) workflowledger.RunSnapshot {
	t.Helper()
	worktreeName := ""
	if !noWorktree {
		worktreeName = "wt-integration"
	}
	run := workflowledger.RunSnapshot{
		RunID: "wfr-" + stackID + "-integration", InvocationKey: stackID + ":" + delivery.IntegrationChunkID,
		WorkflowName: "stacked", WorktreeName: worktreeName, BaseRef: "main", BaseCommit: "basecommit",
		RemoteURL: "https://github.com/o/r.git",
	}
	seedDeliveryPendingRun(t, repo, run, []byte("{}"))
	return run
}
func seedDeliveryPendingRun(t *testing.T, repo workflowledger.Repository, run workflowledger.RunSnapshot, snapshotJSON []byte) {
	t.Helper()
	run.Status = workflowledger.RunStatusPending
	if err := repo.CreateRun(context.Background(), run, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	step := func(to workflowledger.RunStatus) {
		t.Helper()
		stored, err := repo.GetRun(context.Background(), run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, stored.Version, to, nil); err != nil {
			t.Fatal(err)
		}
	}
	step(workflowledger.RunStatusRunning)
	step(workflowledger.RunStatusDeliveryPending)
}
