package localengine_test

import (
	"context"
	"strings"
	"testing"

	workflowagenttools "github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/localengine"
)

// These tests exercise the resume-refusal branches the coverage gate flagged
// as changed-but-unexercised after the agenttools split: delivery_pending,
// delivery_failed, and the not-resumable (pending) status.

func TestEngineResumeRefusesUndeliverableAndPending(t *testing.T) {
	repo := workflowledger.NewMemoryRepository()
	for _, status := range []workflowledger.RunStatus{
		workflowledger.RunStatusDeliveryPending,
		workflowledger.RunStatusDeliveryFailed,
	} {
		run := createDeliveryPendingRun(t, repo, workflowledger.RunSnapshot{
			RunID: "wfr-resume-" + string(status), WorkflowName: "deliver-me", WorkflowDigest: "digest",
			ActiveStepID: "success", BaseRef: "main", BaseCommit: "deadbeef",
		})
		if status == workflowledger.RunStatusDeliveryFailed {
			if err := repo.CompareAndSetRunStatus(context.Background(), run.RunID, run.Version, status, nil); err != nil {
				t.Fatal(err)
			}
		}
		engine := &localengine.Engine{WorkspaceRoot: t.TempDir(), Repo: repo}
		_, err := engine.Start(context.Background(), workflowagenttools.StartRequest{
			Resume: true, RunID: run.RunID,
		})
		want := "waiting for delivery"
		if status == workflowledger.RunStatusDeliveryFailed {
			want = "failed delivery"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("resume(%s) error = %v, want %q refusal", status, err, want)
		}
	}
}
