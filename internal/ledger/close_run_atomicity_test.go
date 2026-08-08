package ledger

// Regression tests for the atomic-close fix: CloseRun published the run
// closure as TWO separate fenced appends (run_closed, then run_status_changed)
// and updated the projection (mem.CloseRun + status write) only after both. A
// transient store failure on the second append returned an error while the
// run_closed row was already durable; the live projection still reported the
// run open, so a retry or the running pool could commit task transitions
// AFTER the durable closure row. Those post-close transitions replay forever,
// so a fresh repository read a canceled/closed run whose tasks moved after
// closure - a durable state machine that contradicts itself (DC-4/DC-9).
//
// The fixed code marshals the terminal transition (status canceled,
// completed_at) into the single run_closed payload, publishes closure and
// terminal status in ONE fenced append, validates in the projection first
// (so a double close writes nothing), and rebuilds the projection from the
// store on append failure (so reads report only what is durable).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// failKindAppendStore wraps a store and fails the next n AppendClaimed calls
// whose event kind matches kind, WITHOUT writing anything to the inner store.
// Later matching appends succeed. It is the deterministic seam for the
// CloseRun atomicity tests: on the pre-fix code a failure armed for
// storageKindRunStatusChanged is exactly the second AppendClaimed call of
// CloseRun (run_closed, then status); on the fixed code no status append
// exists, so the failure never fires. All other methods delegate to the inner
// store through the embedded interface.
type failKindAppendStore struct {
	storage.Store

	mu    sync.Mutex
	kind  string
	failN int
}

// ArmKindFailure schedules the next n AppendClaimed calls with the given kind
// to fail. The tests arm it AFTER the setup writes (CreateRun/CreateTask) have
// reached the store, so the injected failure fires on the CloseRun append
// under test - not on an unrelated setup append that would abort the test
// before it exercises the defect.
func (f *failKindAppendStore) ArmKindFailure(kind string, n int) {
	f.mu.Lock()
	f.kind = kind
	f.failN = n
	f.mu.Unlock()
}

func (f *failKindAppendStore) AppendClaimed(ctx context.Context, e storage.Event, holder string) error {
	f.mu.Lock()
	fail := f.kind != "" && e.Kind == f.kind && f.failN > 0
	if fail {
		f.failN--
		if f.failN == 0 {
			f.kind = "" // failure consumed; later matching appends pass
		}
	}
	f.mu.Unlock()
	if fail {
		return errors.New("injected AppendClaimed failure for kind " + e.Kind)
	}
	return f.Store.AppendClaimed(ctx, e, holder)
}

// TestCloseRunSecondAppendFailureClosesProjection is the regression for the
// confirmed bug: CloseRun's second append (the status transition) fails after
// the run_closed row is durable. Pre-fix, CloseRun returns the injected error
// with the projection still open, so a subsequent CompareAndSetTaskStatus
// succeeds and commits a task_status_changed row AFTER the durable run_closed.
// Post-fix, CloseRun is a single fenced append carrying the terminal
// transition, the armed status-append failure never fires, CloseRun succeeds,
// and the closed projection refuses the CAS with ErrClosed.
func TestCloseRunSecondAppendFailureClosesProjection(t *testing.T) {
	ctx := context.Background()
	store := &failKindAppendStore{Store: storage.NewMemory()}
	repo := NewStorageLedgerRepository(store)
	mustCreateRunWithTask(t, repo, "run-close-second", "t1")

	// Arm a fire-once failure for the run_status_changed append. On the
	// pre-fix code that is exactly the SECOND AppendClaimed call of CloseRun:
	// the run_closed row is durable, the status append fails, CloseRun returns
	// the injected error, and mem.CloseRun was never reached, so the live
	// projection still reports the run open - a retry or the running pool can
	// then commit task transitions after the durable closure.
	store.ArmKindFailure(storageKindRunStatusChanged, 1)
	if err := repo.CloseRun(ctx, "run-close-second"); err != nil {
		t.Fatalf("CloseRun with armed status-append failure: %v", err)
	}

	// (a) The projection is closed: the durable run_closed row must close the
	// live state. On the pre-fix code CloseRun errored before mem.CloseRun, so
	// the projection stayed open. (GetRun derives the run status from the task
	// statuses - the queued task derives queued - so the closed fact is the
	// projection's closed flag, which is exactly what the fix guarantees;
	// GetRun must still serve the run without error.)
	if _, err := repo.GetRun(ctx, "run-close-second"); err != nil {
		t.Fatalf("GetRun after CloseRun: %v", err)
	}
	repo.mem.mu.RLock()
	rec, ok := repo.mem.runs["run-close-second"]
	closed := ok && rec.closed
	repo.mem.mu.RUnlock()
	if !closed {
		t.Fatal("projection reports the run open after CloseRun; the durable run_closed row must close the live state")
	}

	// (b) A post-close task transition must be refused with ErrClosed. On the
	// pre-fix code the projection was still open, so this CAS succeeded and
	// committed a task_status_changed row AFTER the durable run_closed.
	if err := repo.CompareAndSetTaskStatus(ctx, "run-close-second", "t1", 0, string(TaskStatusRunning)); !errors.Is(err, ErrClosed) {
		t.Fatalf("CAS after CloseRun: err = %v, want ErrClosed", err)
	}

	// (c) The store must hold no task_status_changed row whose sequence
	// follows the run_closed row - the durable history must never record a
	// post-close transition.
	rows, err := store.Events(ctx, "run-close-second")
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	closedSeen := false
	for _, row := range rows {
		if row.Kind == storageKindRunClosed {
			closedSeen = true
		}
		if row.Kind == storageKindTaskStatusChanged {
			t.Fatalf("store holds a task_status_changed row after CloseRun: %+v", row)
		}
	}
	if !closedSeen {
		t.Fatal("store holds no run_closed row after CloseRun")
	}
}

// TestCloseRunRetryAfterStatusAppendFailureIsIdempotent covers the retry
// path of the same failure. Pre-fix, the first CloseRun errors with run_closed
// durable and the projection open; the retry then appends a SECOND run_closed
// (duplicate closure, the no-duplicate-transition violation). Post-fix, the
// first CloseRun succeeds (no status append exists), the retry is refused as
// already closed, and the store holds exactly one run_closed row.
func TestCloseRunRetryAfterStatusAppendFailureIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := &failKindAppendStore{Store: storage.NewMemory()}
	repo := NewStorageLedgerRepository(store)
	mustCreateRunWithTask(t, repo, "run-close-retry", "t1")

	store.ArmKindFailure(storageKindRunStatusChanged, 1)
	// Pre-fix: injected error (run_closed durable, projection open). Post-fix:
	// nil (no status append exists to fail).
	_ = repo.CloseRun(ctx, "run-close-retry")

	// The retry is idempotent-safe: an already-closed run must not append a
	// second closure row. Post-fix it is refused as already closed
	// (ErrInvalidTransition per the repository.go contract); pre-fix it
	// succeeds and duplicates the closure row, which the count assertion
	// below rejects.
	if err := repo.CloseRun(ctx, "run-close-retry"); err != nil && !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("retried CloseRun: %v", err)
	}
	if n := countKind(t, store, "run-close-retry", storageKindRunClosed); n != 1 {
		t.Fatalf("store holds %d run_closed rows after CloseRun + retry, want exactly 1 (duplicate closure)", n)
	}
	if err := repo.CompareAndSetTaskStatus(ctx, "run-close-retry", "t1", 0, string(TaskStatusRunning)); !errors.Is(err, ErrClosed) {
		t.Fatalf("CAS after CloseRun: err = %v, want ErrClosed", err)
	}
}

// TestCloseRunTwiceWritesNoSecondRow covers the double-close negative path:
// the second CloseRun must return ErrInvalidTransition (the repository.go
// contract) AND write no rows. Pre-fix, the second call appended a second
// run_closed and a run_status_changed before mem.CloseRun rejected it.
func TestCloseRunTwiceWritesNoSecondRow(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)
	mustCreateRunWithTask(t, repo, "run-close-twice", "t1")

	if err := repo.CloseRun(ctx, "run-close-twice"); err != nil {
		t.Fatalf("first CloseRun: %v", err)
	}
	if err := repo.CloseRun(ctx, "run-close-twice"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second CloseRun: err = %v, want ErrInvalidTransition", err)
	}
	if n := countKind(t, store, "run-close-twice", storageKindRunClosed); n != 1 {
		t.Fatalf("store holds %d run_closed rows after a double close, want exactly 1", n)
	}
	if n := countKind(t, store, "run-close-twice", storageKindRunStatusChanged); n != 0 {
		t.Fatalf("store holds %d run_status_changed rows after CloseRun, want 0", n)
	}
}

// TestCloseRunSuccessKeepsCanceledStatusAndCompletedAt is the positive path:
// a successful CloseRun keeps the existing canceled status + CompletedAt
// behavior, refuses every post-close mutation with ErrClosed, writes exactly
// one run_closed row and no status rows, and a fresh repository replays the
// same terminal state (live equals replay). The task is created terminal
// (canceled) so the run-level status derivation (memory.go fullSnapshot)
// reports canceled, matching the durable payload.
func TestCloseRunSuccessKeepsCanceledStatusAndCompletedAt(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)

	createdAt := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	closeAt := createdAt.Add(10 * time.Minute)
	repo.SetTimeSource(func() time.Time { return closeAt })

	runID := "run-close-success"
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: runID, Status: RunStatusCreated, CreatedAt: createdAt}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t1", Status: string(TaskStatusCanceled), CreatedAt: createdAt}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	if err := repo.CloseRun(ctx, runID); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != RunStatusCanceled {
		t.Fatalf("GetRun status = %q, want %q", run.Status, RunStatusCanceled)
	}
	if run.CompletedAt == nil || !run.CompletedAt.Equal(closeAt) {
		t.Fatalf("GetRun CompletedAt = %v, want %v (the CloseRun instant)", run.CompletedAt, closeAt)
	}

	if err := repo.CompareAndSetTaskStatus(ctx, runID, "t1", 0, string(TaskStatusRunning)); !errors.Is(err, ErrClosed) {
		t.Fatalf("CAS on closed run: err = %v, want ErrClosed", err)
	}
	if err := repo.SetTaskAttempt(ctx, runID, "t1", "att-1", string(TaskStatusRunning), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("SetTaskAttempt on closed run: err = %v, want ErrClosed", err)
	}

	if n := countKind(t, store, runID, storageKindRunClosed); n != 1 {
		t.Fatalf("store holds %d run_closed rows, want exactly 1", n)
	}
	if n := countKind(t, store, runID, storageKindRunStatusChanged); n != 0 {
		t.Fatalf("store holds %d run_status_changed rows, want 0", n)
	}
	if n := countKind(t, store, runID, storageKindTaskStatusChanged); n != 0 {
		t.Fatalf("store holds %d task_status_changed rows, want 0", n)
	}

	// A fresh repository over the same store replays the same terminal state.
	fresh := NewStorageLedgerRepository(store)
	fresh.SetTimeSource(func() time.Time { return closeAt })
	replayed, err := fresh.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("fresh GetRun: %v", err)
	}
	if replayed.Status != RunStatusCanceled {
		t.Fatalf("fresh GetRun status = %q, want %q", replayed.Status, RunStatusCanceled)
	}
	if replayed.CompletedAt == nil || !replayed.CompletedAt.Equal(closeAt) {
		t.Fatalf("fresh GetRun CompletedAt = %v, want %v", replayed.CompletedAt, closeAt)
	}
}

// TestCloseRunSingleAppendFailureRebuildsProjection pins the rebuild path on
// the new single-write shape: a fire-once failure of the run_closed append
// itself must surface the error AND leave the projection equal to what the
// store holds (the run stays open, because the closure row never became
// durable), the retry converges on exactly one closure row, and post-close
// transitions are then refused with ErrClosed.
func TestCloseRunSingleAppendFailureRebuildsProjection(t *testing.T) {
	ctx := context.Background()
	store := &failKindAppendStore{Store: storage.NewMemory()}
	repo := NewStorageLedgerRepository(store)
	mustCreateRunWithTask(t, repo, "run-close-rebuild", "t1")

	store.ArmKindFailure(storageKindRunClosed, 1)
	if err := repo.CloseRun(ctx, "run-close-rebuild"); err == nil || !strings.Contains(err.Error(), "injected AppendClaimed failure") {
		t.Fatalf("CloseRun with failing closure append: err = %v, want injected failure", err)
	}

	// The closure row never reached the store, so the rebuilt projection must
	// keep the run open (the queued task derives a non-terminal run status).
	run, err := repo.GetRun(ctx, "run-close-rebuild")
	if err != nil {
		t.Fatalf("GetRun after failed CloseRun: %v", err)
	}
	if isRunTerminal(run.Status) {
		t.Fatalf("GetRun after failed CloseRun reports terminal %q; the store holds no closure row", run.Status)
	}

	if err := repo.CloseRun(ctx, "run-close-rebuild"); err != nil {
		t.Fatalf("retried CloseRun: %v", err)
	}
	if n := countKind(t, store, "run-close-rebuild", storageKindRunClosed); n != 1 {
		t.Fatalf("store holds %d run_closed rows after the retry, want exactly 1", n)
	}
	if err := repo.CompareAndSetTaskStatus(ctx, "run-close-rebuild", "t1", 0, string(TaskStatusRunning)); !errors.Is(err, ErrClosed) {
		t.Fatalf("CAS after CloseRun: err = %v, want ErrClosed", err)
	}
}

// TestRebuildProjectionRunClosedPayloadShapes is the untrusted-store-row
// decode table for the new run_closed payload: legacy '{}' closes via
// closeRebuiltRun, the new shape applies status and completed_at, a shape
// without status closes via closeRebuiltRun, malformed and empty payloads
// close leniently with no error, duplicate run_closed rows are idempotent,
// and unknown extra fields are ignored.
func TestRebuildProjectionRunClosedPayloadShapes(t *testing.T) {
	runID := "payload-run"
	createdAt := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	ts := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)
	created, err := marshalRunSnapshot(RunSnapshot{RunID: runID, Status: RunStatusRunning, CreatedAt: createdAt})
	if err != nil {
		t.Fatalf("marshal run snapshot: %v", err)
	}

	legacy, err := marshalRunClosed("", nil)
	if err != nil {
		t.Fatalf("marshal legacy run_closed: %v", err)
	}
	newShape, err := marshalRunClosed(string(RunStatusCanceled), &ts)
	if err != nil {
		t.Fatalf("marshal new run_closed: %v", err)
	}
	noStatus, err := marshalRunClosed("", &ts)
	if err != nil {
		t.Fatalf("marshal no-status run_closed: %v", err)
	}

	cases := []struct {
		name           string
		payloads       [][]byte
		desc           string
		statusHint     string
		checkCompleted bool
		wantCompleted  *time.Time
	}{
		{"legacy_empty_object_closes_via_closeRebuiltRun", [][]byte{legacy}, "legacy run_closed", "(closeRebuiltRun on a non-terminal run)", true, nil},
		{"new_shape_applies_status_and_completed_at", [][]byte{newShape}, "new run_closed", "", true, &ts},
		{"shape_without_status_closes_via_closeRebuiltRun", [][]byte{noStatus}, "status-less run_closed", "(closeRebuiltRun)", true, &ts},
		{"malformed_closes_leniently", [][]byte{[]byte("not-json")}, "malformed run_closed", "(malformed payload still closes)", false, nil},
		{"empty_payload_closes", [][]byte{[]byte{}}, "empty run_closed payload", "(empty payload still closes)", false, nil},
		{"duplicate_run_closed_rows_are_idempotent", [][]byte{legacy, newShape}, "duplicate run_closed rows", "", true, &ts},
		{"unknown_extra_fields_are_ignored", [][]byte{[]byte(`{"status":"canceled","completed_at":"2026-08-01T09:30:00Z","extra":"x"}`)}, "run_closed with extra fields", "", true, &ts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := rebuildRunClosedProjection(runID, created, tc.payloads...)
			if err != nil {
				t.Fatalf("RebuildProjection over %s: %v", tc.desc, err)
			}
			if snap.Status != RunStatusCanceled {
				t.Fatalf("status = %q, want %q%s", snap.Status, RunStatusCanceled, tc.statusHint)
			}
			if !tc.checkCompleted {
				return
			}
			if tc.wantCompleted == nil {
				if snap.CompletedAt != nil {
					t.Fatalf("CompletedAt = %v, want nil", snap.CompletedAt)
				}
				return
			}
			if snap.CompletedAt == nil || !snap.CompletedAt.Equal(*tc.wantCompleted) {
				t.Fatalf("CompletedAt = %v, want %v", snap.CompletedAt, *tc.wantCompleted)
			}
		})
	}
}

// rebuildRunClosedProjection replays a run_created event followed by one or
// more run_closed events carrying payloads, and returns the rebuilt snapshot.
func rebuildRunClosedProjection(runID string, created []byte, closedPayloads ...[]byte) (RunSnapshot, error) {
	events := []storage.Event{
		{ID: "e1", RunID: runID, Sequence: 1, Kind: storageKindRunCreated, Payload: created},
	}
	for i, payload := range closedPayloads {
		events = append(events, storage.Event{
			ID:       fmt.Sprintf("e%d", i+2),
			RunID:    runID,
			Sequence: i + 2,
			Kind:     storageKindRunClosed,
			Payload:  payload,
		})
	}
	snap, _, _, err := RebuildProjection(events)
	return snap, err
}

// TestCatchUpToleratesMalformedAndDuplicateRunClosedRows pins that foreign or
// hand-edited run_closed rows (malformed, or duplicated) must not error or
// hang catch-up on either backend: the run still closes and stays fail-closed.
func TestCatchUpToleratesMalformedAndDuplicateRunClosedRows(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name  string
		store func() storage.Store
	}{
		{"memory", func() storage.Store { return storage.NewMemory() }},
		{"sqlite", func() storage.Store { return mustOpenSQLiteStore(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := tc.store()
			runID := "foreign-run"
			created, err := marshalRunSnapshot(RunSnapshot{RunID: runID, Status: RunStatusRunning})
			if err != nil {
				t.Fatalf("marshal run snapshot: %v", err)
			}
			// run_created, then a malformed run_closed row, then a duplicate
			// valid run_closed row: foreign history must catch up without an
			// error or a hang, closing the run.
			for _, evt := range []storage.Event{
				{ID: "se-1", RunID: runID, Sequence: 1, Kind: storageKindRunCreated, Payload: created},
				{ID: "se-2", RunID: runID, Sequence: 2, Kind: storageKindRunClosed, Payload: []byte("not-json")},
				{ID: "se-3", RunID: runID, Sequence: 3, Kind: storageKindRunClosed, Payload: []byte("{}")},
			} {
				if err := store.Append(ctx, evt); err != nil {
					t.Fatalf("append foreign row: %v", err)
				}
			}

			repo := NewStorageLedgerRepository(store)
			run, err := repo.GetRun(ctx, runID)
			if err != nil {
				t.Fatalf("GetRun over foreign run_closed rows: %v", err)
			}
			if run.Status != RunStatusCanceled {
				t.Fatalf("status = %q, want %q", run.Status, RunStatusCanceled)
			}
			// The closed run refuses a new task (fail-closed), and the read
			// path stays live after the duplicate rows.
			if err := repo.CreateTask(ctx, TaskSnapshot{RunID: runID, TaskID: "t9", Status: string(TaskStatusQueued)}); !errors.Is(err, ErrClosed) {
				t.Fatalf("CreateTask on closed run: err = %v, want ErrClosed", err)
			}
		})
	}
}
