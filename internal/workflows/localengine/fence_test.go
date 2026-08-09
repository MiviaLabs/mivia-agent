package localengine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type blockingContentRepository struct {
	workflowledger.Repository
	entered chan struct{}
	release chan struct{}
}

func (r *blockingContentRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	close(r.entered)
	<-r.release
	return r.Repository.StoreContent(ctx, ref, data)
}

// recordingResumeRepository wraps a Repository and records RecordRunResumed
// calls, so tests can assert the fence forwards resume events and fails closed
// for abandoned runs.
type recordingResumeRepository struct {
	workflowledger.Repository
	resumed []string
}

func (r *recordingResumeRepository) RecordRunResumed(ctx context.Context, runID string) error {
	r.resumed = append(r.resumed, runID)
	return r.Repository.RecordRunResumed(ctx, runID)
}

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

func TestAbandonFenceSerializesAbandonWithContentWrite(t *testing.T) {
	inner := workflowledger.NewMemoryRepository()
	run := workflowledger.RunSnapshot{RunID: "wfr-content-fence", Status: workflowledger.RunStatusPending, Version: 1}
	if err := inner.CreateRun(context.Background(), run, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	blocking := &blockingContentRepository{Repository: inner, entered: make(chan struct{}), release: make(chan struct{})}
	fence := newAbandonFence(blocking)
	ctx := workflowledger.ContextWithRunID(context.Background(), run.RunID)
	writeDone := make(chan error, 1)
	go func() { writeDone <- fence.StoreContent(ctx, "ref:fence-race", []byte("output")) }()
	<-blocking.entered
	abandonDone := make(chan struct{})
	go func() {
		fence.abandon(run.RunID)
		close(abandonDone)
	}()
	select {
	case <-abandonDone:
		t.Fatal("abandon completed before the in-flight content write")
	case <-time.After(20 * time.Millisecond):
	}
	close(blocking.release)
	if err := <-writeDone; err != nil {
		t.Fatalf("in-flight content write error = %v", err)
	}
	select {
	case <-abandonDone:
	case <-time.After(time.Second):
		t.Fatal("abandon did not complete after the content write")
	}
	if err := fence.StoreContent(ctx, "ref:fence-after", []byte("output")); err != workflowledger.ErrConflict {
		t.Fatalf("post-abandon content write error = %v, want ErrConflict", err)
	}
}

// TestAbandonFenceRecordRunResumedForwards checks RecordRunResumed delegates to
// the inner repository and travels through the fence mutate path: it reaches
// the inner repo while the run is live, and fails closed with ErrConflict after
// abandon without reaching the inner repo.
func TestAbandonFenceRecordRunResumedForwards(t *testing.T) {
	inner := workflowledger.NewMemoryRepository()
	run := workflowledger.RunSnapshot{RunID: "wfr-resume-fence", Status: workflowledger.RunStatusPending, Version: 1}
	if err := inner.CreateRun(context.Background(), run, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	recording := &recordingResumeRepository{Repository: inner}
	fence := newAbandonFence(recording)
	ctx := context.Background()

	if err := fence.RecordRunResumed(ctx, run.RunID); err != nil {
		t.Fatalf("RecordRunResumed error = %v, want nil", err)
	}
	if len(recording.resumed) != 1 || recording.resumed[0] != run.RunID {
		t.Fatalf("inner RecordRunResumed calls = %v, want [%s]", recording.resumed, run.RunID)
	}

	fence.abandon(run.RunID)
	if err := fence.RecordRunResumed(ctx, run.RunID); err != workflowledger.ErrConflict {
		t.Fatalf("post-abandon RecordRunResumed error = %v, want ErrConflict", err)
	}
	if len(recording.resumed) != 1 {
		t.Fatalf("abandoned RecordRunResumed reached inner repo: calls = %v, want 1", recording.resumed)
	}
}
