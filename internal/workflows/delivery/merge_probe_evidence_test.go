package delivery

import (
	"context"
	"errors"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// failingDeliveriesRepo forces ListDeliveries to fail so ProbeRunMerged's
// pushed-evidence read error path (runPushedRef) is exercised: an unreadable
// ledger must read as "no pushed evidence", never as merged.
type failingDeliveriesRepo struct {
	workflowledger.Repository
}

func (f failingDeliveriesRepo) ListDeliveries(context.Context, string) ([]workflowledger.DeliveryRecord, error) {
	return nil, errors.New("delivery ledger boom")
}

func TestProbeRunMergedTreatsLedgerErrorAsNeverPushed(t *testing.T) {
	_, _, gc, _, _, run, repo := newDeliveryFixture(t)
	run.BaseRef = ""
	run.RemoteURL = "https://github.com/owner/repo.git"
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "probe-error", Status: "pushed", HeadRef: "wf/wt-test", CommitSHA: "abc",
	}); err != nil {
		t.Fatal(err)
	}
	merged, err := ProbeRunMerged(context.Background(), failingDeliveriesRepo{Repository: repo}, run, nil, mergeProbeTestPR{merged: true}, gc)
	if err != nil || merged {
		t.Fatalf("ProbeRunMerged(ledger error) = %v, %v; want false, nil", merged, err)
	}
}

func TestProbeRunMergedRemoteOnlySkipsEmptyCommit(t *testing.T) {
	_, _, gc, _, _, run, repo := newDeliveryFixture(t)
	run.BaseRef = ""
	run.RemoteURL = "https://github.com/owner/repo.git"
	run.WorktreeName = "wt-test"
	// A delivery record without a commit SHA is not pushed evidence.
	if err := repo.UpsertDelivery(context.Background(), workflowledger.DeliveryRecord{
		RunID: run.RunID, IdempotencyKey: "probe-nocommit", Status: "pushed", HeadRef: "wf/wt-test",
	}); err != nil {
		t.Fatal(err)
	}
	merged, err := ProbeRunMerged(context.Background(), repo, run, nil, mergeProbeTestPR{merged: true}, gc)
	if err != nil || merged {
		t.Fatalf("ProbeRunMerged(no commit) = %v, %v; want false, nil", merged, err)
	}
}
