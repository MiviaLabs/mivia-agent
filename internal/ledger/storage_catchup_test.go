package ledger

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// countingStore wraps a store and counts how many event rows are handed to the
// reader, so a test can tell an incremental tail read from a full replay.
type countingStore struct {
	inner storage.Store

	mu            sync.Mutex
	eventsRead    int // rows returned by Events + EventsSince
	fullReadCalls int // calls to Events (whole-history read)
	tailReadCalls int // calls to EventsSince
	probeCalls    int // calls to Changes
}

func (c *countingStore) Append(ctx context.Context, e storage.Event) error {
	return c.inner.Append(ctx, e)
}

func (c *countingStore) AppendClaimed(ctx context.Context, e storage.Event, holder string) error {
	return c.inner.AppendClaimed(ctx, e, holder)
}

func (c *countingStore) Events(ctx context.Context, runID string) ([]storage.Event, error) {
	events, err := c.inner.Events(ctx, runID)
	c.mu.Lock()
	c.fullReadCalls++
	c.eventsRead += len(events)
	c.mu.Unlock()
	return events, err
}

func (c *countingStore) TakeoverClaim(ctx context.Context, runID, holder string) error {
	return c.inner.TakeoverClaim(ctx, runID, holder)
}

func (c *countingStore) EventsSince(ctx context.Context, runID string, afterSequence int) ([]storage.Event, error) {
	events, err := c.inner.EventsSince(ctx, runID, afterSequence)
	c.mu.Lock()
	c.tailReadCalls++
	c.eventsRead += len(events)
	c.mu.Unlock()
	return events, err
}

func (c *countingStore) DeleteRun(ctx context.Context, runID string, throughSequence int) error {
	return c.inner.DeleteRun(ctx, runID, throughSequence)
}

func (c *countingStore) AppendAndDeleteRun(ctx context.Context, tombstone storage.Event, claim storage.Claim) error {
	return c.inner.AppendAndDeleteRun(ctx, tombstone, claim)
}

func (c *countingStore) Changes(ctx context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	c.mu.Lock()
	c.probeCalls++
	c.mu.Unlock()
	return c.inner.Changes(ctx, afterCursor)
}

func (c *countingStore) Count(ctx context.Context) (int, error) { return c.inner.Count(ctx) }

func (c *countingStore) ListRunIDs(ctx context.Context) ([]string, error) {
	return c.inner.ListRunIDs(ctx)
}

func (c *countingStore) ClaimRun(ctx context.Context, runID, holder string) error {
	return c.inner.ClaimRun(ctx, runID, holder)
}

func (c *countingStore) ReleaseClaim(ctx context.Context, runID, holder string) error {
	return c.inner.ReleaseClaim(ctx, runID, holder)
}

func (c *countingStore) ClearClaim(ctx context.Context, runID string) error {
	return c.inner.ClearClaim(ctx, runID)
}

func (c *countingStore) PutContent(ctx context.Context, ref string, data []byte) error {
	return c.inner.PutContent(ctx, ref, data)
}

func (c *countingStore) GetContent(ctx context.Context, ref string) ([]byte, error) {
	return c.inner.GetContent(ctx, ref)
}

func (c *countingStore) Close() error { return c.inner.Close() }

func (c *countingStore) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.eventsRead = 0
	c.fullReadCalls = 0
	c.tailReadCalls = 0
	c.probeCalls = 0
}

func (c *countingStore) reads() (eventsRead, fullReadCalls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventsRead, c.fullReadCalls
}

// TestProjectionSeesWritesFromAnotherRepository is the §5 regression: two
// repository instances over ONE store. B builds its projection first, A then
// writes, and B must observe A's write. Against the one-shot `built` flag B
// reported "not found" and zero runs forever.
func TestProjectionSeesWritesFromAnotherRepository(t *testing.T) {
	store := storage.NewMemory()
	ctx := context.Background()

	a := NewStorageLedgerRepository(store)
	b := NewStorageLedgerRepository(store)

	// B builds its projection BEFORE A writes anything.
	if runs, err := b.ListRuns(ctx); err != nil {
		t.Fatalf("B initial ListRuns: %v", err)
	} else if len(runs) != 0 {
		t.Fatalf("B initial ListRuns: got %d runs, want 0", len(runs))
	}

	if err := a.CreateRun(ctx, "", RunSnapshot{RunID: "r-from-a", Status: RunStatusCreated}); err != nil {
		t.Fatalf("A CreateRun: %v", err)
	}
	if err := a.CreateTask(ctx, TaskSnapshot{RunID: "r-from-a", TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatalf("A CreateTask: %v", err)
	}
	if err := a.CompareAndSetTaskStatus(ctx, "r-from-a", "t1", 0, string(TaskStatusRunning)); err != nil {
		t.Fatalf("A CAS: %v", err)
	}

	run, err := b.GetRun(ctx, "r-from-a")
	if err != nil {
		t.Fatalf("B GetRun after A wrote: %v (projection is stale)", err)
	}
	if run.RunID != "r-from-a" {
		t.Fatalf("B GetRun: RunID = %q, want %q", run.RunID, "r-from-a")
	}

	runs, err := b.ListRuns(ctx)
	if err != nil {
		t.Fatalf("B ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("B ListRuns: got %d runs, want 1", len(runs))
	}

	task, err := b.GetTask(ctx, "r-from-a", "t1")
	if err != nil {
		t.Fatalf("B GetTask: %v", err)
	}
	if task.Status != string(TaskStatusRunning) {
		t.Fatalf("B GetTask: status = %q, want %q", task.Status, TaskStatusRunning)
	}
	if task.Version != 1 {
		t.Fatalf("B GetTask: version = %d, want 1", task.Version)
	}
}

// TestProjectionCatchUpIsIncremental proves catch-up is option ii and not
// option i: after one new event, a read must read one event, not the whole
// history, and a read with nothing new must read no events at all.
func TestProjectionCatchUpIsIncremental(t *testing.T) {
	counting := &countingStore{inner: storage.NewMemory()}
	ctx := context.Background()

	a := NewStorageLedgerRepository(counting)
	b := NewStorageLedgerRepository(counting)

	// Build a history worth re-reading.
	const runs = 4
	const tasksPerRun = 5
	for r := 0; r < runs; r++ {
		runID := fmt.Sprintf("run-%d", r)
		if err := a.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		for i := 0; i < tasksPerRun; i++ {
			taskID := fmt.Sprintf("t%d", i)
			if err := a.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: taskID, Status: string(TaskStatusQueued)}); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			if err := a.CompareAndSetTaskStatus(ctx, runID, taskID, 0, string(TaskStatusRunning)); err != nil {
				t.Fatalf("CAS: %v", err)
			}
		}
	}
	totalEvents := runs * (1 + tasksPerRun*2)

	// B catches up on the whole history once - that read is expected to be big.
	if _, err := b.ListRuns(ctx); err != nil {
		t.Fatalf("B initial ListRuns: %v", err)
	}
	if got, _ := counting.reads(); got < totalEvents {
		t.Fatalf("sanity: initial catch-up read %d events, want >= %d", got, totalEvents)
	}

	// A read with nothing new must not read any events.
	counting.reset()
	if _, err := b.ListRuns(ctx); err != nil {
		t.Fatalf("B steady-state ListRuns: %v", err)
	}
	if got, full := counting.reads(); got != 0 || full != 0 {
		t.Fatalf("steady-state read: %d events via %d whole-history reads, want 0/0", got, full)
	}

	// Exactly one new event from A.
	counting.reset()
	if err := a.CompareAndSetTaskStatus(ctx, "run-0", "t0", 1, string(TaskStatusCompleted)); err != nil {
		t.Fatalf("A CAS: %v", err)
	}
	if _, err := b.ListRuns(ctx); err != nil {
		t.Fatalf("B ListRuns after one new event: %v", err)
	}
	got, full := counting.reads()
	if full != 0 {
		t.Fatalf("catch-up used %d whole-history reads; catch-up must be a bounded tail read", full)
	}
	if got != 1 {
		t.Fatalf("catch-up read %d events for one new event (history is %d); it is not incremental", got, totalEvents)
	}

	// And the new event actually landed.
	task, err := b.GetTask(ctx, "run-0", "t0")
	if err != nil {
		t.Fatalf("B GetTask: %v", err)
	}
	if task.Status != string(TaskStatusCompleted) {
		t.Fatalf("B GetTask: status = %q, want %q", task.Status, TaskStatusCompleted)
	}
}

// TestProjectionCatchUpPreservesOrdering interleaves writes from two
// repositories over one store and requires both projections to converge on the
// same run and task state.
func TestProjectionCatchUpPreservesOrdering(t *testing.T) {
	store := storage.NewMemory()
	ctx := context.Background()

	a := NewStorageLedgerRepository(store)
	b := NewStorageLedgerRepository(store)

	// A non-zero CreatedAt is load-bearing now that projectionState compares it:
	// the projection stamps only what arrives unstamped, so a run created with a
	// zero timestamp would legitimately get a different one in each projection.
	if err := a.CreateRun(ctx, "", RunSnapshot{
		RunID: "run-1", Status: RunStatusCreated, CreatedAt: catchupRunCreatedAt,
	}); err != nil {
		t.Fatalf("A CreateRun: %v", err)
	}
	if err := a.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatalf("A CreateTask: %v", err)
	}
	// B creates a second task on A's run - B must have caught up to see the run.
	if err := b.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t2", Status: string(TaskStatusQueued)}); err != nil {
		t.Fatalf("B CreateTask: %v", err)
	}
	// Interleave status transitions across both repositories.
	if err := a.CompareAndSetTaskStatus(ctx, "run-1", "t1", 0, string(TaskStatusRunning)); err != nil {
		t.Fatalf("A CAS t1 running: %v", err)
	}
	if err := b.CompareAndSetTaskStatus(ctx, "run-1", "t2", 0, string(TaskStatusRunning)); err != nil {
		t.Fatalf("B CAS t2 running: %v", err)
	}
	if err := b.CompareAndSetTaskStatus(ctx, "run-1", "t1", 1, string(TaskStatusCompleted)); err != nil {
		t.Fatalf("B CAS t1 completed: %v", err)
	}
	if err := a.SetTaskOutput(ctx, "run-1", "t1", "ref:out-1", "", ""); err != nil {
		t.Fatalf("A SetTaskOutput: %v", err)
	}
	if err := b.SetTaskAttempt(ctx, "run-1", "t2", "att-1", string(TaskStatusRunning), nil); err != nil {
		t.Fatalf("B SetTaskAttempt: %v", err)
	}
	if err := a.CompareAndSetTaskStatus(ctx, "run-1", "t2", 1, string(TaskStatusFailed)); err != nil {
		t.Fatalf("A CAS t2 failed: %v", err)
	}

	stateA := projectionState(t, a, "run-1")
	stateB := projectionState(t, b, "run-1")
	if stateA != stateB {
		t.Fatalf("projections diverged:\n A: %s\n B: %s", stateA, stateB)
	}

	// A third, fresh repository replaying from scratch must agree too.
	c := NewStorageLedgerRepository(store)
	stateC := projectionState(t, c, "run-1")
	if stateC != stateA {
		t.Fatalf("fresh replay diverged:\n A: %s\n C: %s", stateA, stateC)
	}

	if stateA == "" {
		t.Fatal("empty projection state")
	}
}

// catchupRunCreatedAt is the run creation instant the catch-up tests supply, so
// every projection over the same store agrees on it.
var catchupRunCreatedAt = time.Date(2026, 7, 30, 9, 15, 30, 0, time.UTC)

// projectionState renders the comparable parts of a run's projection.
//
// Run CreatedAt IS compared. It used to be excluded because each projection
// stamped its own, which was the defect plan 21 fixed: the projection now stamps
// only what arrives unstamped, so a supplied timestamp survives both the original
// create and every replay, and any two projections over one store must agree.
func projectionState(t *testing.T, repo *StorageLedgerRepository, runID string) string {
	t.Helper()
	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	tasks, err := repo.ListTasks(ctx, runID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	out := fmt.Sprintf("run=%s status=%s created=%s tasks=%d",
		run.RunID, run.Status, run.CreatedAt.UTC().Format(time.RFC3339Nano), len(tasks))
	sortedTasks := make([]TaskSnapshot, len(tasks))
	copy(sortedTasks, tasks)
	for i := 0; i < len(sortedTasks); i++ {
		for j := i + 1; j < len(sortedTasks); j++ {
			if sortedTasks[j].TaskID < sortedTasks[i].TaskID {
				sortedTasks[i], sortedTasks[j] = sortedTasks[j], sortedTasks[i]
			}
		}
	}
	for _, task := range sortedTasks {
		out += fmt.Sprintf("; task=%s status=%s version=%d out=%s err=%s attempts=%d",
			task.TaskID, task.Status, task.Version, task.OutputRef, task.ErrorRef, len(task.Attempts))
		for _, att := range task.Attempts {
			out += fmt.Sprintf(" [%s:%s]", att.AttemptID, att.Status)
		}
	}
	return out
}

// TestProjectionCatchUpUnderConcurrency exercises catch-up with concurrent
// readers and writers on two repositories over one store. Run with -race.
func TestProjectionCatchUpUnderConcurrency(t *testing.T) {
	store := storage.NewMemory()
	ctx := context.Background()

	writer := NewStorageLedgerRepository(store)
	reader := NewStorageLedgerRepository(store)

	const runs = 20
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < runs; i++ {
			runID := fmt.Sprintf("c-run-%d", i)
			if err := writer.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
				t.Errorf("CreateRun: %v", err)
				return
			}
			if err := writer.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
				t.Errorf("CreateTask: %v", err)
				return
			}
		}
	}()

	// Several concurrent readers on the other repository.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if _, err := reader.ListRuns(ctx); err != nil {
					t.Errorf("ListRuns: %v", err)
					return
				}
				if _, err := reader.GetRun(ctx, "c-run-0"); err != nil && err != ErrNotFound {
					t.Errorf("GetRun: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, err := reader.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != runs {
		t.Fatalf("reader sees %d runs, want %d", len(got), runs)
	}
	for i := 0; i < runs; i++ {
		runID := fmt.Sprintf("c-run-%d", i)
		tasks, err := reader.ListTasks(ctx, runID)
		if err != nil {
			t.Fatalf("ListTasks(%s): %v", runID, err)
		}
		if len(tasks) != 1 {
			t.Fatalf("%s: %d tasks, want 1", runID, len(tasks))
		}
	}
}

// TestConcurrentWritersAreNotReappliedByCatchUp is the regression for the
// window between a writer's store append and its own projection update: a
// catch-up running in that window must not apply the writer's event, or the
// writer's update comes back as a spurious ErrDuplicate. Run with -race.
func TestConcurrentWritersAreNotReappliedByCatchUp(t *testing.T) {
	store := storage.NewMemory()
	ctx := context.Background()
	repo := NewStorageLedgerRepository(store)

	const writers = 24
	var wg sync.WaitGroup
	errs := make(chan error, writers*3)

	// Writers create distinct runs on ONE repository...
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runID := fmt.Sprintf("w-run-%d", i)
			if err := repo.CreateRun(ctx, fmt.Sprintf("key-%d", i), RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
				errs <- fmt.Errorf("CreateRun %s: %w", runID, err)
				return
			}
			if err := repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t1", Status: string(TaskStatusQueued)}); err != nil {
				errs <- fmt.Errorf("CreateTask %s: %w", runID, err)
				return
			}
			if err := repo.CompareAndSetTaskStatus(ctx, runID, "t1", 0, string(TaskStatusRunning)); err != nil {
				errs <- fmt.Errorf("CAS %s: %w", runID, err)
			}
		}(i)
	}

	// ...while readers on the SAME repository keep catch-up running.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if _, err := repo.ListRuns(ctx); err != nil {
					errs <- fmt.Errorf("ListRuns: %w", err)
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent write raced with catch-up: %v", err)
	}

	runs, err := repo.ListRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != writers {
		t.Fatalf("got %d runs, want %d", len(runs), writers)
	}
	for _, run := range runs {
		task, err := repo.GetTask(ctx, run.RunID, "t1")
		if err != nil {
			t.Fatalf("GetTask(%s): %v", run.RunID, err)
		}
		if task.Status != string(TaskStatusRunning) || task.Version != 1 {
			t.Fatalf("%s/t1: status=%q version=%d, want running/1", run.RunID, task.Status, task.Version)
		}
	}

	// Genuine duplicates must still be detected, both by run ID and by key.
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "w-run-0", Status: RunStatusCreated}); err != ErrDuplicate {
		t.Fatalf("duplicate run ID: got %v, want ErrDuplicate", err)
	}
	if err := repo.CreateRun(ctx, "key-0", RunSnapshot{RunID: "other-run", Status: RunStatusCreated}); err != ErrDuplicate {
		t.Fatalf("duplicate idempotency key: got %v, want ErrDuplicate", err)
	}
}

// TestProjectionStateIncludesTimestampsAcrossRebuild is the guard that a rebuild
// reproduces the whole comparable projection, timestamps included, rather than
// just the fields that happened to be compared before.
//
// It renders event id, sequence AND created_at explicitly. projectionState does
// not read any of those - it covers runs and tasks - so a test built only on that
// helper cannot detect a replay path that renumbers sequences or re-stamps
// timestamps. Both halves are asserted here on purpose:
//
//   - created_at must be the original append instant, so a re-stamp fails.
//   - sequence must be the live 1..N, so a replay that renumbers from a fresh
//     counter fails. Sequence is derived rather than durable, and this is the only
//     thing standing between "derived happens to match" and "nobody notices when
//     it stops".
//
// The clocks differ by nine hours so a re-stamp cannot pass as a rounding
// artefact.
func TestProjectionStateIncludesTimestampsAcrossRebuild(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()

	appendInstant := time.Date(2026, 7, 30, 9, 15, 30, 500000000, time.UTC)
	replayInstant := time.Date(2026, 7, 30, 18, 15, 30, 0, time.UTC)

	live := NewStorageLedgerRepository(store)
	live.SetTimeSource(func() time.Time { return appendInstant })
	if err := live.CreateRun(ctx, "", RunSnapshot{
		RunID: "run-1", Status: RunStatusRunning, CreatedAt: catchupRunCreatedAt,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := live.CreateTask(ctx, TaskSnapshot{
		RunID: "run-1", TaskID: "t1", Status: string(TaskStatusQueued), CreatedAt: catchupRunCreatedAt,
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	for i := 1; i <= 4; i++ {
		if err := live.AppendEvent(ctx, LifecycleEvent{
			ID: fmt.Sprintf("evt-%d", i), RunID: "run-1", Kind: "task_started", TaskID: "t1",
		}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}

	wantRun := projectionState(t, live, "run-1")
	wantEvents := eventProjectionState(t, live, "run-1")

	// A fresh repository over the same store replays everything, on a clock nine
	// hours later.
	replayed := NewStorageLedgerRepository(store)
	replayed.SetTimeSource(func() time.Time { return replayInstant })

	if got := projectionState(t, replayed, "run-1"); got != wantRun {
		t.Errorf("run projection diverged across rebuild:\n live:   %s\n replay: %s", wantRun, got)
	}
	if got := eventProjectionState(t, replayed, "run-1"); got != wantEvents {
		t.Errorf("event projection diverged across rebuild:\n live:   %s\n replay: %s\n"+
			"(replay clock is %v; a created= match against that means the projection re-stamped)",
			wantEvents, got, replayInstant)
	}

	// State the expected shape outright, so the comparison above cannot be
	// satisfied by two identically-wrong renderings.
	for i, e := range mustListEvents(t, replayed, "run-1") {
		if e.Sequence != uint64(i+1) {
			t.Errorf("replayed event %d: Sequence = %d, want %d", i, e.Sequence, i+1)
		}
		if !e.CreatedAt.Equal(appendInstant) {
			t.Errorf("replayed event %d: CreatedAt = %v, want the append instant %v",
				i, e.CreatedAt, appendInstant)
		}
	}
}

// eventProjectionState renders a run's lifecycle events as id, sequence and
// created_at. Kept separate from projectionState, which covers runs and tasks.
func eventProjectionState(t *testing.T, repo *StorageLedgerRepository, runID string) string {
	t.Helper()
	var out string
	for _, e := range mustListEvents(t, repo, runID) {
		out += fmt.Sprintf("[seq=%d kind=%s task=%s created=%s]",
			e.Sequence, e.Kind, e.TaskID, e.CreatedAt.UTC().Format(time.RFC3339Nano))
	}
	return out
}

func mustListEvents(t *testing.T, repo *StorageLedgerRepository, runID string) []LifecycleEvent {
	t.Helper()
	events, err := repo.ListEvents(context.Background(), runID)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return events
}
