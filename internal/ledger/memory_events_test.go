package ledger

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Memory-backend timestamp and ordering coverage. The memory repository is the
// default backend (internal/config/load.go), and it is also the projection the
// storage backend replays into, so the "stamp only what arrives unstamped"
// contract has to hold here or it holds nowhere.

// TestMemoryCreateRunPreservesSuppliedCreatedAt covers the run-level half of the
// defect. The coordinator sets a real CreatedAt before calling CreateRun
// (internal/coordinator/spawn.go), and the storage backend marshals that value
// into the durable run_created payload - but the projection then overwrote it
// with its own clock, both on the original create and again on every replay.
//
// Why it is load-bearing: `mivia diagnostics` sorts runs by CreatedAt and
// reports Elapsed as time.Since(CreatedAt) (internal/cli/diagnostics.go). A run
// recovered from disk therefore reported an age of a few milliseconds regardless
// of when it actually started.
func TestMemoryCreateRunPreservesSuppliedCreatedAt(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryLedgerRepository()

	readInstant := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	m.SetTimeSource(func() time.Time { return readInstant })

	original := time.Date(2026, 7, 30, 9, 15, 30, 0, time.UTC)
	if err := m.CreateRun(ctx, "", RunSnapshot{
		RunID: "run-1", Status: RunStatusRunning, CreatedAt: original,
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := m.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !got.CreatedAt.Equal(original) {
		t.Errorf("CreatedAt = %v, want the supplied %v (repository clock is %v)",
			got.CreatedAt, original, readInstant)
	}
}

// TestMemoryCreateRunStampsWhenUnstamped is the other half of the same guard: a
// caller that supplies nothing still gets a real timestamp, so the change cannot
// be satisfied by simply deleting the stamp.
func TestMemoryCreateRunStampsWhenUnstamped(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryLedgerRepository()
	stamp := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	m.SetTimeSource(func() time.Time { return stamp })

	if err := m.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	got, err := m.GetRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !got.CreatedAt.Equal(stamp) {
		t.Errorf("CreatedAt = %v, want the repository clock %v", got.CreatedAt, stamp)
	}
}

// TestMemoryAppendEventStampsOnlyUnstampedEvents is the event-level contract the
// storage backend and the replay path both depend on: a non-zero CreatedAt
// reaching the projection is data, not a suggestion.
//
// No in-process caller supplies one today - every LifecycleEvent construction
// site in internal/coordinator leaves the field zero - so this test covers the
// contract rather than a current caller. That is deliberate: StorageLedgerRepository
// .AppendEvent stamps before marshalling precisely so the durable copy and the
// projection carry one instant, and fromStorageEvent hands the decoded value
// back on replay. Both rely on this guard.
func TestMemoryAppendEventStampsOnlyUnstampedEvents(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryLedgerRepository()
	projectionClock := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	m.SetTimeSource(func() time.Time { return projectionClock })
	if err := m.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	supplied := time.Date(2026, 7, 30, 9, 15, 30, 0, time.UTC)
	if err := m.AppendEvent(ctx, LifecycleEvent{
		ID: "ev-stamped", RunID: "run-1", Kind: "task_completed", CreatedAt: supplied,
	}); err != nil {
		t.Fatalf("AppendEvent stamped: %v", err)
	}
	if err := m.AppendEvent(ctx, LifecycleEvent{
		ID: "ev-unstamped", RunID: "run-1", Kind: "task_completed",
	}); err != nil {
		t.Fatalf("AppendEvent unstamped: %v", err)
	}

	events, err := m.ListEvents(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ListEvents returned %d events, want 2", len(events))
	}
	byID := map[string]LifecycleEvent{}
	for _, e := range events {
		byID[e.ID] = e
	}
	if got := byID["ev-stamped"].CreatedAt; !got.Equal(supplied) {
		t.Errorf("supplied event CreatedAt = %v, want %v (projection clock is %v)",
			got, supplied, projectionClock)
	}
	if got := byID["ev-unstamped"].CreatedAt; !got.Equal(projectionClock) {
		t.Errorf("unstamped event CreatedAt = %v, want the projection clock %v",
			got, projectionClock)
	}
	// Sequence assignment is untouched by the timestamp guard.
	if byID["ev-stamped"].Sequence != 1 || byID["ev-unstamped"].Sequence != 2 {
		t.Errorf("sequences = {stamped:%d unstamped:%d}, want {1 2}",
			byID["ev-stamped"].Sequence, byID["ev-unstamped"].Sequence)
	}
}

// TestListEventsOrderedBySequenceUnderTiedTimestamps guards a property that
// already holds rather than establishing a new one. ListEvents returns
// rec.events in append order, and AppendEvent assigns the sequence immediately
// before appending, so the exposed order is sequence order by construction -
// there is no clock in the read path to degrade.
//
// It is here because making timestamps durable makes clock/sequence disagreement
// reachable, so an ordering that consults the clock would now be wrong in a way
// it previously was not. If anyone later "improves" ListEvents by sorting it on
// CreatedAt, this is what catches it.
//
// Two sub-cases, because ties alone are NOT sufficient evidence: sort.Slice over
// all-equal keys takes pdqsort's insertion path, where less() never returns true
// and nothing moves, so a CreatedAt sort passes a tie-only test. The inverted case
// is the one that bites, and it is not contrived - StorageLedgerRepository.AppendEvent
// stamps CreatedAt before it allocates the store sequence, so under concurrent
// appends to one run a later-sequenced event really can carry an earlier
// timestamp (plan 21 correction C3).
func TestListEventsOrderedBySequenceUnderTiedTimestamps(t *testing.T) {
	frozen := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	// stamps[i] is the CreatedAt supplied for the (i+1)-th appended event.
	cases := []struct {
		name   string
		stamps func(i, count int) time.Time
	}{
		{
			// Every event shares one instant: no key to order by.
			name:   "tied",
			stamps: func(int, int) time.Time { return frozen },
		},
		{
			// Timestamps run BACKWARDS against sequence. A CreatedAt-keyed sort
			// returns the exact reverse of append order.
			name: "inverted",
			stamps: func(i, count int) time.Time {
				return frozen.Add(time.Duration(count-i) * time.Minute)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m := NewMemoryLedgerRepository()
			m.SetTimeSource(func() time.Time { return frozen })
			if err := m.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusRunning}); err != nil {
				t.Fatalf("CreateRun: %v", err)
			}

			const count = 8
			wantIDs := make([]string, 0, count)
			suppliedFor := make(map[string]time.Time, count)
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("ev-%d", i+1)
				wantIDs = append(wantIDs, id)
				suppliedFor[id] = tc.stamps(i, count)
				if err := m.AppendEvent(ctx, LifecycleEvent{
					ID: id, RunID: "run-1", Kind: "task_started", CreatedAt: tc.stamps(i, count),
				}); err != nil {
					t.Fatalf("AppendEvent %s: %v", id, err)
				}
			}

			got, err := m.ListEvents(ctx, "run-1")
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if len(got) != count {
				t.Fatalf("ListEvents returned %d events, want %d", len(got), count)
			}
			for i, e := range got {
				if e.Sequence != uint64(i+1) {
					t.Errorf("position %d: Sequence = %d, want %d - exposed order is not sequence order",
						i, e.Sequence, i+1)
				}
				if e.ID != wantIDs[i] {
					t.Errorf("position %d: ID = %q, want %q - exposed order is not append order",
						i, e.ID, wantIDs[i])
				}
				// Confirm the premise: each event kept the timestamp it was given, so
				// the %s condition really is present and a pass cannot come from the
				// clock happening to agree with the sequence. Keyed on ID, not
				// position, so a reordering failure above does not masquerade as a
				// missing premise here.
				if want := suppliedFor[e.ID]; !e.CreatedAt.Equal(want) {
					t.Errorf("event %s: CreatedAt = %v, want the supplied %v; the %s "+
						"condition this test depends on is not present",
						e.ID, e.CreatedAt, want, tc.name)
				}
			}
		})
	}
}

// TestMemoryAppendEventDuplicateDetectionIsIndexed guards the per-run event-ID
// index that replaced the linear duplicate scan in AppendEvent. The index must
// stay in lockstep with rec.events: every event write flows through
// AppendEvent, which inserts into eventIDs immediately after appending, so
// cardinality and membership must agree after every operation. It also pins the
// per-run scoping the old scan had implicitly: the same ID on a different run
// is a fresh event, not a duplicate.
//
// It is white-box (same package) because the invariant it asserts lives on the
// runRecord internals. It fails before the fix: runRecord had no eventIDs field,
// so the test does not compile against the pre-fix code.
func TestMemoryAppendEventDuplicateDetectionIsIndexed(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryLedgerRepository()
	if err := m.CreateRun(ctx, "", RunSnapshot{RunID: "run-1", Status: RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun run-1: %v", err)
	}
	if err := m.CreateRun(ctx, "", RunSnapshot{RunID: "run-2", Status: RunStatusRunning}); err != nil {
		t.Fatalf("CreateRun run-2: %v", err)
	}

	// Index/events lockstep invariant for one run.
	assertIndexInvariant := func(t *testing.T, runID string) {
		t.Helper()
		m.mu.RLock()
		defer m.mu.RUnlock()
		rec, ok := m.runs[runID]
		if !ok {
			t.Fatalf("run %s missing", runID)
		}
		if len(rec.eventIDs) != len(rec.events) {
			t.Fatalf("index/events cardinality mismatch: len(eventIDs)=%d len(events)=%d",
				len(rec.eventIDs), len(rec.events))
		}
		for _, ev := range rec.events {
			if _, ok := rec.eventIDs[ev.ID]; !ok {
				t.Errorf("appended event ID %q is absent from the index", ev.ID)
			}
		}
	}

	const distinct = 64
	for i := 0; i < distinct; i++ {
		if err := m.AppendEvent(ctx, LifecycleEvent{
			ID: fmt.Sprintf("ev-%d", i), RunID: "run-1", Kind: "task_started",
		}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}
	assertIndexInvariant(t, "run-1")

	// Duplicate append: refused, and the events slice and index are unchanged.
	if err := m.AppendEvent(ctx, LifecycleEvent{
		ID: "ev-0", RunID: "run-1", Kind: "task_started",
	}); err != ErrDuplicate {
		t.Fatalf("duplicate AppendEvent error = %v, want ErrDuplicate", err)
	}
	if got := len(m.runs["run-1"].events); got != distinct {
		t.Errorf("events after duplicate append = %d, want %d", got, distinct)
	}
	assertIndexInvariant(t, "run-1")

	// Fresh ID on the same run: both the slice and the index grow by one.
	if err := m.AppendEvent(ctx, LifecycleEvent{
		ID: "ev-fresh", RunID: "run-1", Kind: "task_completed",
	}); err != nil {
		t.Fatalf("fresh AppendEvent: %v", err)
	}
	if got := len(m.runs["run-1"].events); got != distinct+1 {
		t.Errorf("events after fresh append = %d, want %d", got, distinct+1)
	}
	assertIndexInvariant(t, "run-1")

	// Negative path: the same ID on a different run is not a duplicate.
	if err := m.AppendEvent(ctx, LifecycleEvent{
		ID: "ev-0", RunID: "run-2", Kind: "task_started",
	}); err != nil {
		t.Fatalf("same ID on a different run refused: %v", err)
	}
	assertIndexInvariant(t, "run-2")
}
