package ledger

// Regression tests for the D1 fix: CompareAndSetTaskStatus applied the new
// status and version to the in-memory projection BEFORE the claim-fenced store
// append, and on append failure (claim taken by another holder, or a transient
// store error) returned the error without rolling back or rebuilding. The
// applied watermark is not advanced, so catch-up cannot heal the phantom: the
// instance then served GetTask/ListTasks/GetRun a status and derived run status
// the durable history never recorded.
//
// The fixed code rebuilds the run's projection from the store on append
// failure, so reads report only what the durable history actually holds.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// failOnceAppendStore wraps a store and fails the next N AppendClaimed calls
// WITHOUT writing anything to the inner store. Later appends succeed. It is
// the clean-failure seam for the transient-append-failure tests: a failed
// fenced append must leave the projection equal to what the store holds.
type failOnceAppendStore struct {
	inner storage.Store

	mu       sync.Mutex
	failNext int
}

func (f *failOnceAppendStore) Append(ctx context.Context, e storage.Event) error {
	return f.inner.Append(ctx, e)
}

func (f *failOnceAppendStore) AppendClaimed(ctx context.Context, e storage.Event, holder string) error {
	f.mu.Lock()
	fail := f.failNext > 0
	if fail {
		f.failNext--
	}
	f.mu.Unlock()
	if fail {
		return errors.New("injected AppendClaimed failure")
	}
	return f.inner.AppendClaimed(ctx, e, holder)
}

// ArmFailure schedules the next n AppendClaimed calls to fail. The tests arm
// it AFTER the setup writes (CreateRun/CreateTask) have reached the store, so
// the injected failure fires on the mutation append under test - not on an
// unrelated setup append that would abort the test before it exercises the
// defect.
func (f *failOnceAppendStore) ArmFailure(n int) {
	f.mu.Lock()
	f.failNext = n
	f.mu.Unlock()
}

func (f *failOnceAppendStore) Events(ctx context.Context, runID string) ([]storage.Event, error) {
	return f.inner.Events(ctx, runID)
}

func (f *failOnceAppendStore) EventsSince(ctx context.Context, runID string, afterSequence int) ([]storage.Event, error) {
	return f.inner.EventsSince(ctx, runID, afterSequence)
}

func (f *failOnceAppendStore) DeleteRun(ctx context.Context, runID string, throughSequence int) error {
	return f.inner.DeleteRun(ctx, runID, throughSequence)
}

func (f *failOnceAppendStore) AppendAndDeleteRun(ctx context.Context, tombstone storage.Event, claim storage.Claim) error {
	return f.inner.AppendAndDeleteRun(ctx, tombstone, claim)
}

func (f *failOnceAppendStore) Changes(ctx context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	return f.inner.Changes(ctx, afterCursor)
}

func (f *failOnceAppendStore) ClaimRun(ctx context.Context, runID, holder string) error {
	return f.inner.ClaimRun(ctx, runID, holder)
}

func (f *failOnceAppendStore) ReleaseClaim(ctx context.Context, runID, holder string) error {
	return f.inner.ReleaseClaim(ctx, runID, holder)
}

func (f *failOnceAppendStore) ClearClaim(ctx context.Context, runID string) error {
	return f.inner.ClearClaim(ctx, runID)
}

func (f *failOnceAppendStore) PutContent(ctx context.Context, ref string, data []byte) error {
	return f.inner.PutContent(ctx, ref, data)
}

func (f *failOnceAppendStore) GetContent(ctx context.Context, ref string) ([]byte, error) {
	return f.inner.GetContent(ctx, ref)
}

func (f *failOnceAppendStore) Count(ctx context.Context) (int, error) { return f.inner.Count(ctx) }

func (f *failOnceAppendStore) TakeoverClaim(ctx context.Context, runID, holder string) error {
	return f.inner.TakeoverClaim(ctx, runID, holder)
}

func (f *failOnceAppendStore) ListRunIDs(ctx context.Context) ([]string, error) {
	return f.inner.ListRunIDs(ctx)
}

func (f *failOnceAppendStore) Close() error { return f.inner.Close() }

// TestCompareAndSetTaskStatusFencedCASRestoresProjection is the D1(a)
// regression: repoA claims the run and transitions t1 to running; repoB (which
// does not hold the claim) CASes t1 to completed. The store fence refuses the
// append, but the old code had already applied completed/v2 to repoB's
// projection and derived a terminal run status from it - so repoB served a
// status the durable history never recorded. The fixed code rebuilds repoB's
// projection from the store, so the phantom status and terminal run state
// disappear with the failed append.
func TestCompareAndSetTaskStatusFencedCASRestoresProjection(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repoA := NewStorageLedgerRepository(store)
	repoB := NewStorageLedgerRepository(store)

	now := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	repoA.SetTimeSource(func() time.Time { return now })
	repoB.SetTimeSource(func() time.Time { return now })

	runID := "run-cas-fence"
	if err := repoA.CreateRun(ctx, "key-1", RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: now}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := repoA.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t1", Status: string(TaskStatusQueued), CreatedAt: now}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := repoA.ClaimRun(ctx, runID, "holder-a"); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := repoA.CompareAndSetTaskStatus(ctx, runID, "t1", 0, string(TaskStatusRunning)); err != nil {
		t.Fatalf("repoA CAS to running: %v", err)
	}

	// repoB does not hold the claim: the CAS passes mem (version matches) and
	// then the claim-fenced append refuses it. The error must be ErrClaimHeld.
	if err := repoB.CompareAndSetTaskStatus(ctx, runID, "t1", 1, string(TaskStatusCompleted)); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("repoB CAS on claimed run: err = %v, want ErrClaimHeld", err)
	}

	// The projection must report the durable status (running/v1), not the
	// phantom completed/v2 the failed append never recorded.
	task, err := repoB.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatalf("repoB GetTask: %v", err)
	}
	if task.Status != string(TaskStatusRunning) || task.Version != 1 {
		t.Fatalf("repoB GetTask after fenced CAS = status %q version %d, want running/1", task.Status, task.Version)
	}

	// The run must not have flipped terminal in repoB's projection.
	run, err := repoB.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("repoB GetRun: %v", err)
	}
	if isRunTerminal(run.Status) {
		t.Fatalf("repoB GetRun flipped terminal to %q after a fenced CAS; the durable history has no completed status", run.Status)
	}

	// The store must contain no completed status change.
	rows, err := store.Events(ctx, runID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	for _, row := range rows {
		if row.Kind != storageKindTaskStatusChanged {
			continue
		}
		if _, status, _, _, err := unmarshalStatusChange(row.Payload); err == nil && status == string(TaskStatusCompleted) {
			t.Fatalf("store holds a completed status change the fence refused: %+v", row)
		}
	}

	// The claim holder still serves its own durable view.
	if got, err := repoA.GetTask(ctx, runID, "t1"); err != nil || got.Status != string(TaskStatusRunning) || got.Version != 1 {
		t.Fatalf("repoA GetTask after fenced repoB CAS = %+v err %v, want running/1", got, err)
	}
}

// TestCompareAndSetTaskStatusTransientAppendFailureRestoresProjection is the
// D1(b) regression: a one-shot transient store append failure must surface the
// error AND leave the projection at the pre-CAS state (queued/v0, run
// non-terminal). Before the fix the projection kept running/v1 and derived a
// terminal run status from it.
func TestCompareAndSetTaskStatusTransientAppendFailureRestoresProjection(t *testing.T) {
	ctx := context.Background()
	store := &failOnceAppendStore{inner: storage.NewMemory()}
	repo := NewStorageLedgerRepository(store)

	now := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return now })

	runID := "run-cas-transient"
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: now}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t1", Status: string(TaskStatusQueued), CreatedAt: now}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Arm one transient store append failure AFTER the setup writes reached
	// the store: the AppendClaimed call that fails is the CAS append, not
	// CreateRun or CreateTask. The CAS must surface it and leave the
	// projection at queued/v0.
	store.ArmFailure(1)
	if err := repo.CompareAndSetTaskStatus(ctx, runID, "t1", 0, string(TaskStatusRunning)); err == nil || !strings.Contains(err.Error(), "injected AppendClaimed failure") {
		t.Fatalf("CAS with failing append: err = %v, want injected failure", err)
	}

	task, err := repo.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != string(TaskStatusQueued) || task.Version != 0 {
		t.Fatalf("GetTask after failed append = status %q version %d, want queued/0", task.Status, task.Version)
	}

	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if isRunTerminal(run.Status) {
		t.Fatalf("GetRun flipped terminal to %q after a failed append", run.Status)
	}

	// No task_status_changed row may exist: the append failed before writing.
	rows, err := store.Events(ctx, runID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	for _, row := range rows {
		if row.Kind == storageKindTaskStatusChanged {
			t.Fatalf("store holds a task_status_changed row after a failed append: %+v", row)
		}
	}
}

// TestCompareAndSetTaskStatusHolderRetryAfterFailedAppend is the D1(c)
// regression: after a failed append the rebuild must restore the task version,
// so the holder's retry CAS at the SAME expected version succeeds - it must
// not fail with ErrConflict against a phantom version, and a fresh repository
// over the same store must replay the same durable state.
func TestCompareAndSetTaskStatusHolderRetryAfterFailedAppend(t *testing.T) {
	ctx := context.Background()
	store := &failOnceAppendStore{inner: storage.NewMemory()}
	repo := NewStorageLedgerRepository(store)

	now := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	repo.SetTimeSource(func() time.Time { return now })

	runID := "run-cas-retry"
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: now}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t1", Status: string(TaskStatusQueued), CreatedAt: now}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Arm one transient store append failure AFTER the setup writes reached
	// the store, so the first CAS append is the call that fails.
	store.ArmFailure(1)
	// The first CAS fails on the transient append error; the rebuild restores
	// the projection to queued/v0, so the retry at the same expected version
	// must succeed - not fail with ErrConflict on a phantom version.
	if err := repo.CompareAndSetTaskStatus(ctx, runID, "t1", 0, string(TaskStatusRunning)); err == nil {
		t.Fatal("first CAS unexpectedly succeeded")
	}
	if err := repo.CompareAndSetTaskStatus(ctx, runID, "t1", 0, string(TaskStatusRunning)); err != nil {
		t.Fatalf("holder retry CAS after rebuild: %v", err)
	}

	task, err := repo.GetTask(ctx, runID, "t1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Status != string(TaskStatusRunning) || task.Version != 1 {
		t.Fatalf("GetTask after retry = status %q version %d, want running/1", task.Status, task.Version)
	}

	// A fresh repository over the same store replays the same durable state.
	fresh := NewStorageLedgerRepository(store.inner)
	fresh.SetTimeSource(func() time.Time { return now })
	if got, err := fresh.GetTask(ctx, runID, "t1"); err != nil || got.Status != string(TaskStatusRunning) || got.Version != 1 {
		t.Fatalf("fresh repo GetTask = %+v err %v, want running/1", got, err)
	}
}
