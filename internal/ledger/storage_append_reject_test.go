package ledger

// Regression tests for the D2 fix: the append-side methods (AppendEvent,
// CreateTask, SetTaskOutput, SetTaskAttempt) wrote the durable row BEFORE the
// projection validated. When the projection rejected the write (duplicate
// event ID, payload over maxEventPayload, missing run or task), the store kept
// a row the live projection never applied, and a fresh repository replayed it -
// a single oversized lifecycle row wedged catch-up for every later operation.
//
// The fixed methods validate and apply in the projection first and roll back on
// append failure, so a rejected write never reaches the store and a failed
// fenced append leaves no projection trace.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func mustOpenSQLiteStore(t *testing.T) storage.Store {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newRejectTestRepo(t *testing.T) (*StorageLedgerRepository, storage.Store) {
	t.Helper()
	store := storage.NewMemory()
	return NewStorageLedgerRepository(store), store
}

func mustCreateRunWithTask(t *testing.T, repo *StorageLedgerRepository, runID, taskID string) {
	t.Helper()
	ctx := context.Background()
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: taskID, Status: string(TaskStatusQueued)}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
}

func countKind(t *testing.T, store storage.Store, runID, kind string) int {
	t.Helper()
	rows, err := store.Events(context.Background(), runID)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	n := 0
	for _, row := range rows {
		if row.Kind == kind {
			n++
		}
	}
	return n
}

func TestAppendEventDuplicateIDLeavesNoStoreRow(t *testing.T) {
	ctx := context.Background()
	repo, store := newRejectTestRepo(t)
	mustCreateRunWithTask(t, repo, "run-1", "t1")

	if err := repo.AppendEvent(ctx, LifecycleEvent{ID: "evt-1", RunID: "run-1", Kind: "task_running", TaskID: "t1"}); err != nil {
		t.Fatalf("first AppendEvent: %v", err)
	}
	if err := repo.AppendEvent(ctx, LifecycleEvent{ID: "evt-1", RunID: "run-1", Kind: "task_running", TaskID: "t1"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second AppendEvent: err = %v, want ErrDuplicate", err)
	}

	if n := countKind(t, store, "run-1", storageKindLifecycleEvent); n != 1 {
		t.Fatalf("store holds %d lifecycle rows after a duplicate AppendEvent, want 1", n)
	}

	// A fresh repository replays exactly one event, so the documented
	// idempotent-retry contract survives a restart.
	fresh := NewStorageLedgerRepository(store)
	events, err := fresh.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("fresh repo ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("fresh repo replays %d events, want 1", len(events))
	}
}

func TestAppendEventOversizedPayloadLeavesNoStoreRow(t *testing.T) {
	ctx := context.Background()
	repo, store := newRejectTestRepo(t)
	mustCreateRunWithTask(t, repo, "run-1", "t1")

	oversized := make([]byte, maxEventPayload+1)
	if err := repo.AppendEvent(ctx, LifecycleEvent{ID: "evt-big", RunID: "run-1", Kind: "task_completed", TaskID: "t1", Payload: oversized}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized AppendEvent: err = %v, want payload-exceeds error", err)
	}
	if n := countKind(t, store, "run-1", storageKindLifecycleEvent); n != 0 {
		t.Fatalf("store holds %d lifecycle rows after a refused oversized event, want 0", n)
	}

	// A fresh repository must still catch up cleanly (before the fix the
	// orphan row made applyTail fail and wedged every later operation) and
	// list zero events.
	fresh := NewStorageLedgerRepository(store)
	if runs, err := fresh.ListRuns(ctx); err != nil {
		t.Fatalf("fresh repo ListRuns after refused oversized event: %v", err)
	} else if len(runs) != 1 {
		t.Fatalf("fresh repo sees %d runs, want 1", len(runs))
	}
	events, err := fresh.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("fresh repo ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("fresh repo lists %d events, want 0", len(events))
	}
}

func TestAppendEventMissingRunLeavesNoStoreRow(t *testing.T) {
	ctx := context.Background()
	repo, store := newRejectTestRepo(t)

	if err := repo.AppendEvent(ctx, LifecycleEvent{ID: "evt-orphan", RunID: "run-missing", Kind: "task_running", TaskID: "t1"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AppendEvent on missing run: err = %v, want ErrNotFound", err)
	}
	if n := countKind(t, store, "run-missing", storageKindLifecycleEvent); n != 0 {
		t.Fatalf("store holds %d lifecycle rows for a missing run, want 0", n)
	}
}

func TestCreateTaskRejectedLeavesNoStoreRow(t *testing.T) {
	ctx := context.Background()
	repo, store := newRejectTestRepo(t)
	mustCreateRunWithTask(t, repo, "run-1", "t1")

	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-1", TaskID: "t1", Status: string(TaskStatusQueued)}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate CreateTask: err = %v, want ErrDuplicate", err)
	}
	if n := countKind(t, store, "run-1", storageKindTaskCreated); n != 1 {
		t.Fatalf("store holds %d task_created rows after a duplicate CreateTask, want 1", n)
	}

	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: "run-missing", TaskID: "t9", Status: string(TaskStatusQueued)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateTask on missing run: err = %v, want ErrNotFound", err)
	}
	if n := countKind(t, store, "run-missing", storageKindTaskCreated); n != 0 {
		t.Fatalf("store holds %d task_created rows for a missing run, want 0", n)
	}

	fresh := NewStorageLedgerRepository(store)
	tasks, err := fresh.ListTasks(ctx, "run-1")
	if err != nil {
		t.Fatalf("fresh repo ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("fresh repo replays %d tasks, want 1", len(tasks))
	}
}

func TestSetTaskOutputMissingRunOrTaskLeavesNoStoreRow(t *testing.T) {
	ctx := context.Background()
	repo, store := newRejectTestRepo(t)
	mustCreateRunWithTask(t, repo, "run-1", "t1")

	if err := repo.SetTaskOutput(ctx, "run-missing", "t1", "ref:o", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetTaskOutput on missing run: err = %v, want ErrNotFound", err)
	}
	if err := repo.SetTaskOutput(ctx, "run-1", "t-missing", "ref:o", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetTaskOutput on missing task: err = %v, want ErrNotFound", err)
	}
	if n := countKind(t, store, "run-1", storageKindTaskOutputSet); n != 0 {
		t.Fatalf("store holds %d task_output_set rows after refused SetTaskOutput, want 0", n)
	}
	if n := countKind(t, store, "run-missing", storageKindTaskOutputSet); n != 0 {
		t.Fatalf("store holds %d task_output_set rows for a missing run, want 0", n)
	}
}

func TestSetTaskAttemptMissingRunOrTaskLeavesNoStoreRow(t *testing.T) {
	ctx := context.Background()
	repo, store := newRejectTestRepo(t)
	mustCreateRunWithTask(t, repo, "run-1", "t1")

	if err := repo.SetTaskAttempt(ctx, "run-missing", "t1", "att-1", string(TaskStatusRunning), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetTaskAttempt on missing run: err = %v, want ErrNotFound", err)
	}
	if err := repo.SetTaskAttempt(ctx, "run-1", "t-missing", "att-1", string(TaskStatusRunning), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetTaskAttempt on missing task: err = %v, want ErrNotFound", err)
	}
	if n := countKind(t, store, "run-1", storageKindTaskAttempt); n != 0 {
		t.Fatalf("store holds %d task_attempt rows after refused SetTaskAttempt, want 0", n)
	}
	if n := countKind(t, store, "run-missing", storageKindTaskAttempt); n != 0 {
		t.Fatalf("store holds %d task_attempt rows for a missing run, want 0", n)
	}
}

// TestSetTaskAttemptClosedRunRejected pins the fail-closed guard that
// SetTaskAttempt shares with the memory repository: a closed run rejects new
// attempts with ErrClosed, and the rejection leaves no store row and no
// projection trace. The D2 reorder previously applied the attempt to the
// projection directly (applyAttempt without the rec.closed check) and then
// appended the durable row, so a closed run silently accepted new attempts.
func TestSetTaskAttemptClosedRunRejected(t *testing.T) {
	ctx := context.Background()
	repo, store := newRejectTestRepo(t)
	mustCreateRunWithTask(t, repo, "run-1", "t1")

	if err := repo.CloseRun(ctx, "run-1"); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}
	if err := repo.SetTaskAttempt(ctx, "run-1", "t1", "att-1", string(TaskStatusRunning), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("SetTaskAttempt on closed run: err = %v, want ErrClosed", err)
	}
	if n := countKind(t, store, "run-1", storageKindTaskAttempt); n != 0 {
		t.Fatalf("store holds %d task_attempt rows after refused SetTaskAttempt on a closed run, want 0", n)
	}
	// The projection must not carry the attempt either.
	task, err := repo.GetTask(ctx, "run-1", "t1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(task.Attempts) != 0 {
		t.Fatalf("closed-run task holds %d attempts after a refused SetTaskAttempt, want 0", len(task.Attempts))
	}
}

// TestAppendEventFencedRollbackLeavesNoTrace pins the rollback half of the D2
// fix: a claim-fenced append that fails AFTER the projection applied the event
// must leave the projection empty and the store rowless - the reordered
// projection-first code must still roll back on append failure.
func TestAppendEventFencedRollbackLeavesNoTrace(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repoA := NewStorageLedgerRepository(store)
	repoB := NewStorageLedgerRepository(store)
	mustCreateRunWithTask(t, repoA, "run-1", "t1")

	if err := repoA.ClaimRun(ctx, "run-1", "holder-a"); err != nil {
		t.Fatalf("repoA ClaimRun: %v", err)
	}
	if err := repoB.AppendEvent(ctx, LifecycleEvent{ID: "evt-b", RunID: "run-1", Kind: "task_running", TaskID: "t1"}); !errors.Is(err, ErrClaimHeld) {
		t.Fatalf("repoB AppendEvent on claimed run: err = %v, want ErrClaimHeld", err)
	}

	events, err := repoB.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("repoB ListEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("repoB projection holds %d events after a fenced append, want 0", len(events))
	}
	if n := countKind(t, store, "run-1", storageKindLifecycleEvent); n != 0 {
		t.Fatalf("store holds %d lifecycle rows after a fenced append, want 0", n)
	}
}

// TestAppendEventBoundarySizesAndSequences pins the boundary contract that must
// survive the reorder: 1024-byte payloads append and replay, 1025-byte payloads
// are refused, empty IDs and empty payloads append, and sequences stay 1..N.
func TestAppendEventBoundarySizesAndSequences(t *testing.T) {
	ctx := context.Background()
	repo := NewStorageLedgerRepository(storage.NewMemory())
	mustCreateRunWithTask(t, repo, "run-1", "t1")

	exact := make([]byte, maxEventPayload)
	if err := repo.AppendEvent(ctx, LifecycleEvent{ID: "evt-1024", RunID: "run-1", Kind: "task_completed", TaskID: "t1", Payload: exact}); err != nil {
		t.Fatalf("1024-byte payload AppendEvent: %v", err)
	}
	over := make([]byte, maxEventPayload+1)
	if err := repo.AppendEvent(ctx, LifecycleEvent{ID: "evt-1025", RunID: "run-1", Kind: "task_completed", TaskID: "t1", Payload: over}); err == nil {
		t.Fatal("1025-byte payload AppendEvent succeeded, want refusal")
	}
	// Empty ID and empty payload are both accepted.
	if err := repo.AppendEvent(ctx, LifecycleEvent{ID: "", RunID: "run-1", Kind: "task_running", TaskID: "t1"}); err != nil {
		t.Fatalf("empty-ID AppendEvent: %v", err)
	}
	if err := repo.AppendEvent(ctx, LifecycleEvent{ID: "evt-empty", RunID: "run-1", Kind: "task_running", TaskID: "t1"}); err != nil {
		t.Fatalf("empty-payload AppendEvent: %v", err)
	}

	events, err := repo.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("projection holds %d events, want 3", len(events))
	}
	for i, evt := range events {
		if evt.Sequence != uint64(i+1) {
			t.Fatalf("event %d: Sequence = %d, want %d", i, evt.Sequence, i+1)
		}
	}

	// A fresh repository replays the accepted events with the same payload
	// shapes and sequence numbering. It replays a store that never saw the
	// rejected 1025 event, so the anchor also passes on the pre-fix code.
	freshStore := storage.NewMemory()
	fresh := NewStorageLedgerRepository(freshStore)
	mustCreateRunWithTask(t, fresh, "run-1", "t1")
	if err := fresh.AppendEvent(ctx, LifecycleEvent{ID: "evt-1024", RunID: "run-1", Kind: "task_completed", TaskID: "t1", Payload: exact}); err != nil {
		t.Fatalf("fresh 1024-byte AppendEvent: %v", err)
	}
	if err := fresh.AppendEvent(ctx, LifecycleEvent{ID: "evt-empty", RunID: "run-1", Kind: "task_running", TaskID: "t1"}); err != nil {
		t.Fatalf("fresh empty-payload AppendEvent: %v", err)
	}
	replayed := NewStorageLedgerRepository(freshStore)
	got, err := replayed.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("fresh repo ListEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("fresh repo replays %d events, want 2", len(got))
	}
	if len(got[0].Payload) != maxEventPayload {
		t.Fatalf("replayed 1024-byte payload = %d bytes, want %d", len(got[0].Payload), maxEventPayload)
	}
	if len(got[1].Payload) != 0 {
		t.Fatalf("replayed empty payload = %d bytes, want 0", len(got[1].Payload))
	}
	for i, evt := range got {
		if evt.Sequence != uint64(i+1) {
			t.Fatalf("replayed event %d: Sequence = %d, want %d", i, evt.Sequence, i+1)
		}
	}
}

// Structured-input probes: rows written directly to the store (a foreign or
// hand-edited history) must decode during catch-up without a panic or a hang.
// These pin existing behavior and stay green after the reorder.

func TestStoreRejectsEmptyPayloadRowOnBothBackends(t *testing.T) {
	ctx := context.Background()
	for _, store := range []storage.Store{storage.NewMemory(), mustOpenSQLiteStore(t)} {
		if err := store.Append(ctx, storage.Event{ID: "se-empty", RunID: "r", Sequence: 1, Kind: storageKindRunCreated}); err == nil {
			t.Fatalf("store accepted a row with an empty payload")
		}
	}
}

func TestCatchUpRejectsMalformedTaskPayloads(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, kind string
	}{
		{"task_created", storageKindTaskCreated},
		{"task_status_changed", storageKindTaskStatusChanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemory()
			if err := store.Append(ctx, storage.Event{
				ID: "se-bad", RunID: "corrupt-run", Sequence: 1,
				Kind: tc.kind, Payload: []byte("not-json"),
			}); err != nil {
				t.Fatalf("append corrupt row: %v", err)
			}
			repo := NewStorageLedgerRepository(store)
			if _, err := repo.ListTasks(ctx, "corrupt-run"); err == nil {
				t.Fatalf("ListTasks over a corrupt %s payload: got nil, want error", tc.kind)
			} else if !strings.Contains(err.Error(), "corrupt-run") {
				t.Fatalf("error %q does not name the failing run", err)
			}
		})
	}
}

func TestCatchUpToleratesDuplicateTaskCreatedRow(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	if err := store.Append(ctx, storage.Event{ID: "se-0", RunID: "dup-run", Sequence: 1, Kind: storageKindRunCreated, Payload: []byte(`{"RunID":"dup-run","Status":"created"}`)}); err != nil {
		t.Fatal(err)
	}
	payload, err := marshalTaskSnapshot(TaskSnapshot{RunID: "dup-run", TaskID: "t1", Status: string(TaskStatusQueued)})
	if err != nil {
		t.Fatalf("marshal task snapshot: %v", err)
	}
	if err := store.Append(ctx, storage.Event{ID: "se-1", RunID: "dup-run", Sequence: 2, Kind: storageKindTaskCreated, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, storage.Event{ID: "se-2", RunID: "dup-run", Sequence: 3, Kind: storageKindTaskCreated, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	repo := NewStorageLedgerRepository(store)
	tasks, err := repo.ListTasks(ctx, "dup-run")
	if err != nil {
		t.Fatalf("ListTasks over a duplicated task_created row: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("projection holds %d tasks after a duplicated task_created row, want 1", len(tasks))
	}
}

// TestOversizedInjectedLifecycleRowErrorsCleanly is the documented residual: the
// D2 fix prevents NEW oversized rows, but a row already in the store still makes
// the first read error cleanly (naming the failing run) rather than hanging or
// corrupting state.
func TestOversizedInjectedLifecycleRowErrorsCleanly(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	if err := store.Append(ctx, storage.Event{ID: "se-run", RunID: "big-run", Sequence: 1, Kind: storageKindRunCreated, Payload: []byte(`{"RunID":"big-run","Status":"created"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, storage.Event{ID: "se-big", RunID: "big-run", Sequence: 2, Kind: storageKindLifecycleEvent, Payload: make([]byte, maxEventPayload+1)}); err != nil {
		t.Fatal(err)
	}
	repo := NewStorageLedgerRepository(store)
	if _, err := repo.ListEvents(ctx, "big-run"); err == nil {
		t.Fatal("ListEvents over an oversized injected row: got nil, want a clean error")
	} else if !strings.Contains(err.Error(), "big-run") {
		t.Fatalf("error %q does not name the failing run", err)
	}
}
