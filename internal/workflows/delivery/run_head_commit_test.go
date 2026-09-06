package delivery

import (
	"context"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestRunHeadCommitReturnsPushedCommit covers the read path the merge oracle
// uses: with a pushed delivery record carrying a commit SHA, RunHeadCommit
// returns that SHA.
func TestRunHeadCommitReturnsPushedCommit(t *testing.T) {
	ctx := context.Background()
	repo := workflowledger.NewMemoryRepository()
	run := workflowledger.RunSnapshot{RunID: "wfr-head-commit", Status: workflowledger.RunStatusPending}
	if err := repo.CreateRun(ctx, run, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertDelivery(ctx, workflowledger.DeliveryRecord{
		RunID:          run.RunID,
		IdempotencyKey: DeliveryKey(run.RunID, "digest"),
		Mode:           "draft",
		BaseRef:        "main",
		Status:         "pushed",
		CommitSHA:      "c0ffee",
	}); err != nil {
		t.Fatal(err)
	}

	if got := RunHeadCommit(ctx, repo, run); got != "c0ffee" {
		t.Fatalf("RunHeadCommit = %q, want \"c0ffee\"", got)
	}

	// An empty list (unknown run) yields the empty-string miss.
	if got := RunHeadCommit(ctx, repo, workflowledger.RunSnapshot{RunID: "wfr-unknown"}); got != "" {
		t.Fatalf("RunHeadCommit(unknown run) = %q, want \"\"", got)
	}
}
