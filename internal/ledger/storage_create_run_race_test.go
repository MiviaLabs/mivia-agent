package ledger

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// parkingAppendStore wraps a store and parks (blocks) the AppendClaimed call
// that appends a run_created row for run-y until release() is called. Every
// other call - run-x's run_created append and all catch-up reads (Changes,
// EventsSince, Events) - passes through to the inner store. Like
// failingAppendStore it does NOT implement FencedLeaseStore, so
// appendStoreEvent uses the plain AppendClaimed path, and CreateRun's
// catch-up probe runs before the parked append.
type parkingAppendStore struct {
	storage.Store

	releaseOnce sync.Once
	parkOnce    sync.Once
	parked      chan struct{}
	releaseCh   chan struct{}
}

func newParkingAppendStore(inner storage.Store) *parkingAppendStore {
	return &parkingAppendStore{
		Store:     inner,
		parked:    make(chan struct{}),
		releaseCh: make(chan struct{}),
	}
}

func (p *parkingAppendStore) AppendClaimed(ctx context.Context, e storage.Event, holder string) error {
	if e.Kind == storageKindRunCreated && e.RunID == "run-y" {
		p.parkOnce.Do(func() { close(p.parked) })
		<-p.releaseCh
	}
	return p.Store.AppendClaimed(ctx, e, holder)
}

func (p *parkingAppendStore) release() {
	p.releaseOnce.Do(func() { close(p.releaseCh) })
}

// TestCreateRunIdempotencyKeyGateClosesSharedStoreRace is the regression guard
// for LEDGER-1 (HIGH): the idempotency key used to be enforced only by each
// repository instance's own in-memory projection. Two instances over the same
// store (the documented shared-workspace deployment) that CreateRun with the
// same key but different run IDs both passed their private mem checks and both
// committed a run_created row - the keyed work executed twice and the loser
// left an unreachable durable row. The fix mints the run_created event ID
// deterministically from the key, so the store's events.id PRIMARY KEY /
// global ID map is the durable same-key backstop: the loser's append collides
// and surfaces as storage.ErrDuplicate, which CreateRun's append-failure path
// maps to ErrDuplicate and rolls back.
//
// The race is made deterministic with a parking store: repoB parks at its
// run_created append (after its catch-up probe and its private registration of
// K -> run-y), repoA commits run-x under the same key, and only then is repoB
// released. Pre-fix, repoB's append succeeds (distinct random event IDs), so
// the test fails on every assertion below.
func TestCreateRunIdempotencyKeyGateClosesSharedStoreRace(t *testing.T) {
	ctx := context.Background()
	store := newParkingAppendStore(storage.NewMemory())
	repoA := NewStorageLedgerRepository(store)
	repoB := NewStorageLedgerRepository(store)
	defer store.release()

	done := make(chan error, 1)
	go func() {
		done <- repoB.CreateRun(ctx, "K", RunSnapshot{RunID: "run-y", Status: RunStatusCreated})
	}()

	select {
	case <-store.parked:
	case <-time.After(10 * time.Second):
		t.Fatal("repoB never reached its run_created append")
	}

	// repoA commits first: the same key, a different run ID.
	if err := repoA.CreateRun(ctx, "K", RunSnapshot{RunID: "run-x", Status: RunStatusCreated}); err != nil {
		t.Fatalf("repoA CreateRun(K, run-x): %v", err)
	}

	// Release repoB: its append must collide on the deterministic event ID.
	store.release()
	if err := <-done; err != ErrDuplicate {
		t.Fatalf("repoB CreateRun(K, run-y) after repoA committed the key: got %v, want ErrDuplicate (the keyed run_created committed twice)", err)
	}

	// The refused run leaves no durable row.
	if evts, err := store.Store.Events(ctx, "run-y"); err != nil {
		t.Fatal(err)
	} else if len(evts) != 0 {
		t.Fatalf("store contains %d durable rows for the refused run-y: %+v", len(evts), evts)
	}

	// The store holds exactly one run_created row, carrying key K, under the
	// deterministic ID, for run-x.
	evts, err := store.Store.Events(ctx, "run-x")
	if err != nil {
		t.Fatal(err)
	}
	var created []storage.Event
	for _, e := range evts {
		if e.Kind == storageKindRunCreated {
			created = append(created, e)
		}
	}
	if len(created) != 1 {
		t.Fatalf("store holds %d run_created rows for key K: %+v", len(created), evts)
	}
	if created[0].RunID != "run-x" || created[0].ID != "create-run:K" {
		t.Fatalf("run_created row = %+v, want RunID run-x with deterministic id create-run:K", created[0])
	}
	snap, err := unmarshalRunSnapshot(created[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if snap.IdempotencyKey != "K" {
		t.Fatalf("run_created payload IdempotencyKey = %q, want K", snap.IdempotencyKey)
	}

	// Both instances converge on run-x for the key. repoA serves it from its
	// own projection; repoB must catch up over the store after its rollback.
	for name, repo := range map[string]*StorageLedgerRepository{"repoA": repoA, "repoB": repoB} {
		got, err := repo.GetRunByIdempotencyKey(ctx, "K")
		if err != nil {
			t.Fatalf("%s GetRunByIdempotencyKey(K): %v", name, err)
		}
		if got.RunID != "run-x" {
			t.Fatalf("%s resolves key K to %q, want run-x (the losing run_created was not rolled back)", name, got.RunID)
		}
	}
}

// parkingChangesStore wraps a store and parks the next armed Changes call on
// the way BACK from the inner store: the caller resumes holding a probe result
// computed BEFORE any write made while parked. That is exactly the staleness
// the PC-1 admission probe must recover from - a legacy keyed run_created row
// committed after a repository's last catch-up probe but before its append.
// Every other call passes through to the inner store.
type parkingChangesStore struct {
	storage.Store

	armChange   atomic.Bool
	parkOnce    sync.Once
	parked      chan struct{}
	releaseOnce sync.Once
	releaseCh   chan struct{}
}

func newParkingChangesStore(inner storage.Store) *parkingChangesStore {
	return &parkingChangesStore{
		Store:     inner,
		parked:    make(chan struct{}),
		releaseCh: make(chan struct{}),
	}
}

// armNext parks the next Changes call after the inner store has returned.
func (p *parkingChangesStore) armNext() {
	p.armChange.Store(true)
}

func (p *parkingChangesStore) Changes(ctx context.Context, afterCursor uint64) (map[string]int, uint64, error) {
	maxSequences, cursor, err := p.Store.Changes(ctx, afterCursor)
	if p.armChange.Swap(false) {
		p.parkOnce.Do(func() { close(p.parked) })
		<-p.releaseCh
	}
	return maxSequences, cursor, err
}

func (p *parkingChangesStore) release() {
	p.releaseOnce.Do(func() { close(p.releaseCh) })
}

// TestCreateRunLegacyKeyedRowRefusedAtAdmission is the regression guard for
// PC-1 (panel finding): the deterministic "create-run:"+key event ID only
// collides with rows written by the fix itself. A run_created row written by
// a pre-fix binary carries a RANDOM event ID with the key inside the payload,
// so a second keyed CreateRun - in the same or another instance - cannot
// collide with it, both rows become durable, and keyed work runs twice. The
// fix adds an admission-time re-probe: before registering the key in the
// projection, CreateRun re-checks the store and folds any run_created row
// committed since the top-of-method catch-up, so mem.CreateRun refuses on the
// already-durable key.
//
// The race is made deterministic with a parking store that blocks the armed
// Changes call on its way back, freezing the catch-up probe result BEFORE the
// legacy row is written: the admission then decides against a stale probe,
// exactly the window a pre-fix instance leaves open. Without the re-probe the
// admission registers and appends the keyed run, so the test fails on the
// ErrDuplicate and duplicate-row assertions below.
func TestCreateRunLegacyKeyedRowRefusedAtAdmission(t *testing.T) {
	ctx := context.Background()
	inner := storage.NewMemory()
	parked := newParkingChangesStore(inner)
	repoB := NewStorageLedgerRepository(parked)
	defer parked.release()

	// Freeze repoB's first catch-up probe: its result must predate the legacy
	// write below.
	parked.armNext()
	done := make(chan error, 1)
	go func() {
		done <- repoB.CreateRun(ctx, "K", RunSnapshot{RunID: "run-y", Status: RunStatusCreated})
	}()

	select {
	case <-parked.parked:
	case <-time.After(10 * time.Second):
		t.Fatal("repoB never reached its catch-up probe")
	}

	// A pre-fix binary commits the same key under a RANDOM event ID while
	// repoB's probe result is stale.
	legacySnap := RunSnapshot{RunID: "run-legacy", Status: RunStatusCreated, IdempotencyKey: "K"}
	payload, err := marshalRunSnapshot(legacySnap)
	if err != nil {
		t.Fatal(err)
	}
	if err := inner.Append(ctx, storage.Event{
		ID:       newStorageEventID(),
		RunID:    "run-legacy",
		Sequence: 1,
		Kind:     storageKindRunCreated,
		Payload:  payload,
	}); err != nil {
		t.Fatal(err)
	}

	parked.release()
	if err := <-done; err != ErrDuplicate {
		t.Fatalf("CreateRun(K, run-y) over a durable legacy keyed row: got %v, want ErrDuplicate (the keyed run_created committed twice)", err)
	}

	// The refused run leaves no durable row, the store holds exactly one keyed
	// run_created row (the legacy random-ID one), and repoB converges on it.
	assertNoDurableRows(t, inner, "run-y")
	keyed := keyedRunCreatedEvents(t, inner, "K")
	if len(keyed) != 1 {
		t.Fatalf("store holds %d run_created rows carrying key K, want exactly 1 (the legacy row): %+v", len(keyed), keyed)
	}
	if keyed[0].RunID != "run-legacy" || keyed[0].ID == "create-run:K" {
		t.Fatalf("keyed run_created row = %+v, want the legacy random-ID row for run-legacy", keyed[0])
	}

	got, err := repoB.GetRunByIdempotencyKey(ctx, "K")
	if err != nil {
		t.Fatalf("GetRunByIdempotencyKey(K): %v", err)
	}
	if got.RunID != "run-legacy" {
		t.Fatalf("key K resolves to %q, want run-legacy", got.RunID)
	}
	if _, err := repoB.GetRun(ctx, "run-y"); err != ErrNotFound {
		t.Fatalf("refused run-y is visible in the projection: %v", err)
	}

	// Negative-path guard: the probe refuses only the colliding key; a
	// different key still admits on the same repo.
	if err := repoB.CreateRun(ctx, "K2", RunSnapshot{RunID: "run-z", Status: RunStatusCreated}); err != nil {
		t.Fatalf("CreateRun with a different key after the legacy refusal: %v", err)
	}
}

// assertNoDurableRows fails the test unless the store holds no events for the
// given run.
func assertNoDurableRows(t *testing.T, store storage.Store, runID string) {
	t.Helper()
	evts, err := store.Events(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evts) != 0 {
		t.Fatalf("store contains %d durable rows for %s: %+v", len(evts), runID, evts)
	}
}

// keyedRunCreatedEvents returns every run_created event across all runs whose
// payload carries the given idempotency key.
func keyedRunCreatedEvents(t *testing.T, store storage.Store, key string) []storage.Event {
	t.Helper()
	ctx := context.Background()
	runIDs, err := store.ListRunIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var keyed []storage.Event
	for _, runID := range runIDs {
		evts, err := store.Events(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evts {
			if e.Kind != storageKindRunCreated {
				continue
			}
			snap, err := unmarshalRunSnapshot(e.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if snap.IdempotencyKey == key {
				keyed = append(keyed, e)
			}
		}
	}
	return keyed
}

// TestCreateRunEmptyKeyRunsCoexist pins the empty-key side of the fix: only a
// non-empty idempotency key gets the deterministic event ID, so two keyless
// CreateRuns on one store must not collide with each other (they keep the
// random newStorageEventID()).
func TestCreateRunEmptyKeyRunsCoexist(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	repo := NewStorageLedgerRepository(store)

	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-a", Status: RunStatusCreated}); err != nil {
		t.Fatalf("CreateRun(empty key, run-a): %v", err)
	}
	if err := repo.CreateRun(ctx, "", RunSnapshot{RunID: "run-b", Status: RunStatusCreated}); err != nil {
		t.Fatalf("CreateRun(empty key, run-b): %v", err)
	}
	if _, err := repo.GetRun(ctx, "run-a"); err != nil {
		t.Fatalf("GetRun(run-a): %v", err)
	}
	if _, err := repo.GetRun(ctx, "run-b"); err != nil {
		t.Fatalf("GetRun(run-b): %v", err)
	}
	// Both keyless runs are durable.
	for _, runID := range []string{"run-a", "run-b"} {
		evts, err := store.Events(ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		if len(evts) != 1 || evts[0].Kind != storageKindRunCreated {
			t.Fatalf("%s durable events = %+v, want exactly one run_created", runID, evts)
		}
	}
}
