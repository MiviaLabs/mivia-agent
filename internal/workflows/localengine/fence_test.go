package localengine

import (
	"context"
	"fmt"
	"sync"
	"testing"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// TestAbandonFenceConcurrentMapAccess races abandon/clearAbandon/isAbandoned
// under the race detector. A bare delete of abandoned without f.mu fails this.
func TestAbandonFenceConcurrentMapAccess(t *testing.T) {
	f := newAbandonFence(workflowledger.NewMemoryRepository())
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		id := fmt.Sprintf("wfr-%d", i%8)
		wg.Add(3)
		go func(runID string) {
			defer wg.Done()
			f.abandon(runID)
		}(id)
		go func(runID string) {
			defer wg.Done()
			f.clearAbandon(runID)
		}(id)
		go func(runID string) {
			defer wg.Done()
			_ = f.isAbandoned(runID)
		}(id)
	}
	wg.Wait()
}

func TestAbandonFenceRejectsEveryRunMutation(t *testing.T) {
	inner := workflowledger.NewMemoryRepository()
	run := workflowledger.RunSnapshot{RunID: "wfr-fence", Status: workflowledger.RunStatusPending, Version: 1}
	if err := inner.CreateRun(context.Background(), run, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	fence := newAbandonFence(inner)
	fence.abandon(run.RunID)
	if err := fence.CompareAndSetRunStatus(context.Background(), run.RunID, run.Version, workflowledger.RunStatusRunning, nil); err != workflowledger.ErrConflict {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	ctx := workflowledger.ContextWithRunID(context.Background(), run.RunID)
	if err := fence.StoreContent(ctx, "ref:fence", []byte("output")); err != workflowledger.ErrConflict {
		t.Fatalf("store content error = %v, want ErrConflict", err)
	}
}
