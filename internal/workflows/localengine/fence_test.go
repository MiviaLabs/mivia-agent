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

// TestFenceGetRunClaimPassesThrough pins that the read-only claim probe passes
// through the abandon fence unfenced: it reports the held claim even for an
// abandoned run, because observing a claim is never a mutation.
func TestFenceGetRunClaimPassesThrough(t *testing.T) {
	ctx := context.Background()
	inner := workflowledger.NewMemoryRepository()
	fence := newAbandonFence(inner)
	if err := inner.ClaimRun(ctx, "wfr-fc", "h1"); err != nil {
		t.Fatal(err)
	}
	holder, _, ok, err := fence.GetRunClaim(ctx, "wfr-fc")
	if err != nil || !ok || holder != "h1" {
		t.Fatalf("GetRunClaim = holder %q ok %v err %v, want h1 true nil", holder, ok, err)
	}
	fence.abandon("wfr-fc")
	if _, _, ok, err := fence.GetRunClaim(ctx, "wfr-fc"); err != nil || !ok {
		t.Fatalf("GetRunClaim after abandon = ok %v err %v, want the claim still readable", ok, err)
	}
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

// TestAbandonFenceDoesNotSerializeUnrelatedRuns pins that fenced writes for
// two different run IDs run concurrently: run A's write can block in flight
// while run B's write completes, and vice versa. Before sharding the lock per
// run, abandonFence held one process-wide mutex across every fenced write, so
// this would deadlock (both writes block on the single f.mu, and neither
// completes until the other times out this test).
func TestAbandonFenceDoesNotSerializeUnrelatedRuns(t *testing.T) {
	inner := workflowledger.NewMemoryRepository()
	for _, runID := range []string{"wfr-shard-a", "wfr-shard-b"} {
		run := workflowledger.RunSnapshot{RunID: runID, Status: workflowledger.RunStatusPending, Version: 1}
		if err := inner.CreateRun(context.Background(), run, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	blocking := &blockingContentRepository{Repository: inner, entered: make(chan struct{}), release: make(chan struct{})}
	fence := newAbandonFence(blocking)

	// Start run A's write and let it block inside blockingContentRepository.
	ctxA := workflowledger.ContextWithRunID(context.Background(), "wfr-shard-a")
	aDone := make(chan error, 1)
	go func() { aDone <- fence.StoreContent(ctxA, "ref:shard-a", []byte("a")) }()
	<-blocking.entered

	// Run B's write must complete without waiting for A's write to unblock:
	// CompareAndSetRunStatus does not go through blockingContentRepository, so
	// it only observes cross-run serialization, never blockingContentRepository
	// itself.
	bDone := make(chan error, 1)
	go func() {
		bDone <- fence.CompareAndSetRunStatus(context.Background(), "wfr-shard-b", 1, workflowledger.RunStatusRunning, nil)
	}()
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("run B write error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run B's fenced write blocked behind run A's in-flight write; the fence still serializes unrelated runs")
	}

	close(blocking.release)
	if err := <-aDone; err != nil {
		t.Fatalf("run A write error = %v", err)
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

// TestAbandonFenceGetRunClaimPassesThrough pins that the claim probe is a pure
// read: GetRunClaim reaches the inner repository unchanged both before and
// after abandon, because observing a claim is never a mutation the fence must
// guard.
func TestAbandonFenceGetRunClaimPassesThrough(t *testing.T) {
	inner := workflowledger.NewMemoryRepository()
	fence := newAbandonFence(inner)
	ctx := context.Background()
	if err := inner.ClaimRun(ctx, "wfr-fc", "h1"); err != nil {
		t.Fatal(err)
	}
	holder, at, ok, err := fence.GetRunClaim(ctx, "wfr-fc")
	if err != nil || !ok || holder != "h1" {
		t.Fatalf("GetRunClaim through the fence = holder %q ok %v err %v, want h1 true nil", holder, ok, err)
	}
	if at.IsZero() {
		t.Fatal("GetRunClaim through the fence lost acquiredAt")
	}
	fence.abandon("wfr-fc")
	if _, _, ok, err := fence.GetRunClaim(ctx, "wfr-fc"); err != nil || !ok {
		t.Fatalf("GetRunClaim after abandon must still pass through: ok %v err %v", ok, err)
	}
}
