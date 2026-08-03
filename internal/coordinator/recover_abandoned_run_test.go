package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// abandonedRunCoordinator builds a coordinator whose ledger holds a keyed run
// in the D4 abandoned state: status 'created' with zero tasks - exactly what a
// pre-fix crash between the durable run_created append and the in-memory
// registration (or between CreateRun and the first CreateTask) leaves behind.
// Seeding goes through the public ledger API: create the keyed run, close the
// writing repository, and replay it through a fresh one (a process restart).
func abandonedRunCoordinator(t *testing.T) *coordinator {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemory()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	seed := ledger.NewStorageLedgerRepository(store)
	seed.SetTimeSource(func() time.Time { return now })
	if err := seed.CreateRun(ctx, "K", ledger.RunSnapshot{
		RunID: "run-abandoned", Status: ledger.RunStatusCreated, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed abandoned run: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed repo: %v", err)
	}

	fresh := ledger.NewStorageLedgerRepository(store)
	fresh.SetTimeSource(func() time.Time { return now })

	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	return New(fresh, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)
}

// TestRecoverByIdempotencyKeyTreatsAbandonedCreationAsNotFound is the
// defense-in-depth guard for D4: a keyed run recovered in status 'created'
// with zero tasks is an abandoned creation that never executed anything.
// Returning it as a dedup hit would hand the caller a dead handle whose Join
// reports errRecoveredRunNotResumable, permanently bricking the key. The guard
// must report not-found so Spawn re-creates and executes the work.
func TestRecoverByIdempotencyKeyTreatsAbandonedCreationAsNotFound(t *testing.T) {
	c := abandonedRunCoordinator(t)
	ctx := context.Background()

	h, found, err := c.recoverByIdempotencyKey(ctx, "K", "fingerprint")
	if err != nil {
		t.Fatalf("recoverByIdempotencyKey: %v", err)
	}
	if found || h != nil {
		t.Fatalf("abandoned creation (status created, zero tasks) resolved as a dedup hit: found=%v handle=%v; "+
			"layer 2 must treat it as not-found so Spawn re-creates and executes the work", found, h)
	}
}

// TestRecoverByIdempotencyKeyStillDedupsRunsWithTasks pins the boundary of the
// abandoned-run guard: ONLY 'created + zero tasks' is treated as abandoned. A
// running or completed run with tasks - and even a 'created' run that already
// has a task - must still dedup onto its own handle.
func TestRecoverByIdempotencyKeyStillDedupsRunsWithTasks(t *testing.T) {
	ctx := context.Background()
	repo := ledger.NewMemoryLedgerRepository()
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	seedRun := func(runID, key string, status ledger.RunStatus) {
		t.Helper()
		if err := repo.CreateRun(ctx, key, ledger.RunSnapshot{RunID: runID, Status: status, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		task := ledger.TaskSnapshot{
			RunID: runID, TaskID: runID + "-t1", Status: string(ledger.TaskStatusQueued), Version: 1,
			HandlerName: "worker", AgentName: "worker", AgentDigest: "test-digest", Input: json.RawMessage(`{"p":1}`),
		}
		if err := repo.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	seedRun("run-running", "key-running", ledger.RunStatusRunning)
	seedRun("run-created-with-task", "key-created-task", ledger.RunStatusCreated)
	seedRun("run-completed", "key-completed", ledger.RunStatusCompleted)

	c := newIdempotencyCoordinator(repo).(*coordinator)

	for _, tc := range []struct{ key, runID string }{
		{"key-running", "run-running"},
		{"key-created-task", "run-created-with-task"}, // created but WITH a task is not abandoned
		{"key-completed", "run-completed"},
	} {
		h, found, err := c.recoverByIdempotencyKey(ctx, tc.key, "fingerprint")
		if err != nil {
			t.Fatalf("%s: recoverByIdempotencyKey: %v", tc.key, err)
		}
		if !found || h == nil {
			t.Fatalf("%s: run %s must still dedup (found=%v handle=%v)", tc.key, tc.runID, found, h)
		}
		if h.runID != tc.runID {
			t.Fatalf("%s: handle runID = %q, want %q", tc.key, h.runID, tc.runID)
		}
	}
}

// TestSpawnAgainstAbandonedRunDoesNotBrickTheKey exercises the spawn-with-key
// path against the seeded abandoned run. The D4 failure mode is that Spawn
// returns the dead 'created' handle and Join thereafter reports
// errRecoveredRunNotResumable - the idempotency key is permanently bricked and
// the work never runs. After the layer-2 guard, recoverByIdempotencyKey
// reports not-found for the abandoned run, so Spawn must not return that dead
// handle.
//
// Note on the boundary: the ledger's idempotency index still maps K to the
// replayed abandoned row, so Spawn's re-create is refused with ErrDuplicate
// and Spawn returns an error rather than a fresh run. Reclaiming the key for
// the new run would need a ledger change outside this task's file scope. The
// invariant pinned here is the one the guard owns: the key is never surfaced
// as a Join-able handle whose result errors with errRecoveredRunNotResumable.
func TestSpawnAgainstAbandonedRunDoesNotBrickTheKey(t *testing.T) {
	c := abandonedRunCoordinator(t)
	ctx := context.Background()

	h, err := c.Spawn(ctx, []subagents.Task{idempotencyTask()}, "K")
	if err != nil {
		// Fixed path: Spawn refuses cleanly (the key is still held by the
		// replayed abandoned row). The key must not be bricked onto a dead
		// handle.
		if errors.Is(err, errRecoveredRunNotResumable) {
			t.Fatalf("Spawn returned errRecoveredRunNotResumable against an abandoned run: %v", err)
		}
		return
	}
	// Guard-less path: Spawn returned a handle. If it is the abandoned run's
	// handle and Join reports errRecoveredRunNotResumable, the key is bricked.
	res, joinErr := c.Join(ctx, h)
	if joinErr != nil {
		t.Fatalf("Join: %v", joinErr)
	}
	if res != nil && res.Err != nil && errors.Is(res.Err, errRecoveredRunNotResumable) {
		t.Fatalf("D4 regression: Spawn deduped onto the abandoned run %q and the key is bricked: %v", h.runID, res.Err)
	}
}

// TestSpawnReclaimsAbandonedKeyAndExecutesWork pins R2B-2: an abandoned keyed
// run (status 'created', zero tasks - a crash between the durable run_created
// append and the first task event) must be RECLAIMED, not merely reported
// not-found. The guard's not-found alone cannot let Spawn re-create: the
// replayed row keeps the key registered in the projection's idemLookup, so
// createAndStartRun's CreateRun(K) returns ErrDuplicate, a second
// recoverByIdempotencyKey is again not-found, and Spawn returns 'create run:
// duplicate' - the work never executes. Reclaiming deletes the abandoned run
// so CreateRun(K) succeeds and the same key dispatches the work for real.
func TestSpawnReclaimsAbandonedKeyAndExecutesWork(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	// The seeded run must be OLDER than the abandoned-run grace period for the
	// round-3 gate to consider it abandoned. A relative age (not a fixed
	// calendar date) keeps the reclaim path testable regardless of wall clock.
	now := time.Now().Add(-2 * abandonedRunGracePeriod)
	seedAbandonedRun(t, store, now)

	fresh := ledger.NewStorageLedgerRepository(store)
	fresh.SetTimeSource(func() time.Time { return now })

	invoked := make(chan struct{}, 1)
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "worker", invoker(func(context.Context, runtime.Request) (json.RawMessage, error) {
		invoked <- struct{}{}
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	c := New(fresh, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)

	h, err := c.Spawn(ctx, []subagents.Task{idempotencyTask()}, "K")
	if err != nil {
		t.Fatalf("Spawn against abandoned keyed run: %v; want the abandoned row reclaimed so the work executes (R2B-2)", err)
	}
	if h.runID == "run-abandoned" {
		t.Fatalf("Spawn resolved to the abandoned run %q; want a fresh run under the same key", h.runID)
	}

	joinCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := c.Join(joinCtx, h)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if res.Err != nil {
		t.Fatalf("run result error: %v", res.Err)
	}

	select {
	case <-invoked:
	default:
		t.Fatal("worker was never invoked: the abandoned-key spawn did not dispatch its task")
	}

	snap, err := c.Inspect(ctx, h)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if snap.Status != ledger.RunStatusCompleted {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusCompleted)
	}
	if len(snap.Tasks) != 1 || snap.Tasks[0].Status != string(ledger.TaskStatusCompleted) {
		t.Fatalf("tasks = %#v, want exactly one completed task", snap.Tasks)
	}

	// The key must now resolve to the NEW run, not the reclaimed abandoned row.
	assertKeyResolvesTo(t, fresh, "K", h.runID)

	// A fresh replay must converge on the same answer: the durable tombstone
	// dropped the abandoned row and the key maps to the new run.
	replayed := ledger.NewStorageLedgerRepository(store)
	replayed.SetTimeSource(func() time.Time { return now })
	assertKeyResolvesTo(t, replayed, "K", h.runID)
}

// seedAbandonedRun creates an old, unclaimed, zero-task keyed run over store and
// closes the seed repository so the durable events replay into a fresh repo.
// The seed repo borrows the store: its Close simulates a process restart (a
// fresh projection replaying the durable events) without closing the store
// underneath the fresh repository, unlike the memory backend where Close is a
// no-op.
func seedAbandonedRun(t *testing.T, store storage.Store, now time.Time) {
	t.Helper()
	seed := ledger.NewBorrowedStorageLedgerRepository(store)
	seed.SetTimeSource(func() time.Time { return now })
	if err := seed.CreateRun(context.Background(), "K", ledger.RunSnapshot{
		RunID: "run-abandoned", Status: ledger.RunStatusCreated, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed abandoned run: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed repo: %v", err)
	}
}

// assertKeyResolvesTo fails unless repo resolves key to wantRunID.
func assertKeyResolvesTo(t *testing.T, repo ledger.LedgerRepository, key, wantRunID string) {
	t.Helper()
	ctx := context.Background()
	byKey, err := repo.GetRunByIdempotencyKey(ctx, key)
	if err != nil {
		t.Fatalf("GetRunByIdempotencyKey(%s): %v", key, err)
	}
	if byKey.RunID != wantRunID {
		t.Fatalf("GetRunByIdempotencyKey(%s) = %q, want %q", key, byKey.RunID, wantRunID)
	}
}

// reclaimGuardCoordinator seeds a keyed run stranded in the 'created + zero
// tasks' state with a caller-controlled CreatedAt and optional claim holder,
// then replays it through a fresh repository (a process restart), mirroring
// abandonedRunCoordinator. The claim is seeded at the STORE level (not through
// the seed repository) so the seed's Close does not release it as one of its
// own tracked claims.
func reclaimGuardCoordinator(t *testing.T, createdAt time.Time, claimHolder string) (*coordinator, string) {
	t.Helper()
	ctx := context.Background()
	store := storage.NewMemory()

	seed := ledger.NewStorageLedgerRepository(store)
	if err := seed.CreateRun(ctx, "K", ledger.RunSnapshot{
		RunID: "run-guarded", Status: ledger.RunStatusCreated, CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("seed created run: %v", err)
	}
	if claimHolder != "" {
		if err := store.ClaimRun(ctx, "run-guarded", claimHolder); err != nil {
			t.Fatalf("seed claim: %v", err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed repo: %v", err)
	}

	fresh := ledger.NewStorageLedgerRepository(store)
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	return New(fresh, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator), "run-guarded"
}

// TestReclaimSkipsClaimedLiveRun is the round-3 claim guard: a 'created + zero
// tasks' run CLAIMED by another holder is live (or recoverable by its holder),
// never abandoned. Recovery must NOT delete it - the run must still resolve
// afterwards. The old-created-at row is the load-bearing one: only the claim
// probe (not the grace gate) protects that run, so it fails if the probe is
// removed. Pre-fix, reclaimAbandonedRun deleted both unconditionally, killing a
// live creator that had durably created and claimed its run but not yet written
// its first task.
//
// R4-1: a claimed run is also a contention signal - another process either
// owns the run or is mid-reclaim (holding its probe claim) - so recovery must
// report ErrIdempotencyKeyContended, never a 'fresh run' signal that would let
// the caller race a second execution.
func TestReclaimSkipsClaimedLiveRun(t *testing.T) {
	for _, tc := range []struct {
		name      string
		createdAt time.Time
	}{
		{"recent-created-at", time.Now()},
		{"old-created-at", time.Now().Add(-2 * abandonedRunGracePeriod)}, // old enough to pass the grace gate
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, runID := reclaimGuardCoordinator(t, tc.createdAt, "other-holder")
			ctx := context.Background()

			h, found, err := c.recoverByIdempotencyKey(ctx, "K", "fingerprint")
			if !errors.Is(err, ErrIdempotencyKeyContended) {
				t.Fatalf("recoverByIdempotencyKey for claimed run: err=%v, want %v (R4-1); a claimed run is live or being reclaimed and the caller must back off", err, ErrIdempotencyKeyContended)
			}
			if found || h != nil {
				t.Fatalf("claimed live run resolved as a dedup hit (found=%v handle=%v); a claimed run must be left to its holder", found, h)
			}
			if _, err := c.repo.GetRun(ctx, runID); err != nil {
				t.Fatalf("claimed live run %q was deleted by the abandoned-run guard: %v; DeleteRun must not fire on a claimed run", runID, err)
			}
		})
	}
}

// TestReclaimSkipsYoungUnclaimedRun is the round-3 grace gate: a 'created +
// zero tasks' run younger than the grace period is never reclaimed, even when
// the claim probe cannot see a holder. A live creator occupies exactly this
// state between the durable CreateRun and the first durable CreateTask
// (sub-millisecond), and the claim row appears only after ClaimRun, so age - not
// the claim probe - is what keeps a mid-creation run alive. Pre-fix, the guard
// deleted it.
//
// R4-1: a young run is a live creator mid-creation, so recovery reports
// ErrIdempotencyKeyContended - the caller must retry until the creator's run is
// durably visible and dedup onto it, never create a second run.
func TestReclaimSkipsYoungUnclaimedRun(t *testing.T) {
	c, runID := reclaimGuardCoordinator(t, time.Now(), "")
	ctx := context.Background()

	h, found, err := c.recoverByIdempotencyKey(ctx, "K", "fingerprint")
	if !errors.Is(err, ErrIdempotencyKeyContended) {
		t.Fatalf("recoverByIdempotencyKey for young unclaimed run: err=%v, want %v (R4-1); a live creator holds K mid-creation and the caller must back off", err, ErrIdempotencyKeyContended)
	}
	if found || h != nil {
		t.Fatalf("young unclaimed run resolved as a dedup hit (found=%v handle=%v)", found, h)
	}
	if _, err := c.repo.GetRun(ctx, runID); err != nil {
		t.Fatalf("young unclaimed run %q was deleted by the abandoned-run guard: %v; a run younger than the grace period must not be reclaimed", runID, err)
	}
}

// TestReclaimProceedsForOldUnclaimedRun pins the positive case behind the
// round-3 guard: an OLD 'created + zero tasks' run with NO claim is provably
// abandoned - no holder has claimed it for the whole grace period - so recovery
// deletes it and the idempotency key is freed for a fresh spawn.
func TestReclaimProceedsForOldUnclaimedRun(t *testing.T) {
	c, runID := reclaimGuardCoordinator(t, time.Now().Add(-2*abandonedRunGracePeriod), "")
	ctx := context.Background()

	h, found, err := c.recoverByIdempotencyKey(ctx, "K", "fingerprint")
	if err != nil {
		t.Fatalf("recoverByIdempotencyKey: %v", err)
	}
	if found || h != nil {
		t.Fatalf("old unclaimed run resolved as a dedup hit (found=%v handle=%v); it must be reclaimed", found, h)
	}
	if _, err := c.repo.GetRun(ctx, runID); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("old unclaimed run %q was not reclaimed: GetRun = %v, want ErrNotFound", runID, err)
	}
}

// concurrentSpawnCoordinators seeds an abandoned keyed run X (status 'created',
// zero tasks) over a shared sqlite store and returns coordinator A, coordinator
// B, and a factory for FURTHER fresh coordinators over the same store. A and B
// replay the same durable store - the two-process R4-1 setup. invoked, when
// non-nil, is called once per worker invocation across every coordinator so a
// test can count the total executions of the keyed work.
//
// The fresh factory exists because the task's deterministic recipe has the
// second process start over the same store AFTER the first has reclaimed and
// created its run: a fresh projection (empty at first catch-up) applies the new
// run's run_created before the old run's tombstone with no key conflict, so it
// converges onto the winner's run. A coordinator that had already read the
// abandoned row would instead catch up with the tombstone sorted after the new
// run (run IDs are random base32, uppercase, and sort before "run-abandoned") -
// a pre-existing ledger projection-ordering limitation outside this task's file
// scope (see internal/ledger/storage_projection.go catchUp ordering).
func concurrentSpawnCoordinators(t *testing.T, createdAt time.Time, invoked func()) (*coordinator, *coordinator, func() *coordinator) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Seed through a borrowed repository: its Close simulates a process restart
	// (a fresh projection replaying the durable events) without closing the
	// store underneath the live repositories.
	seed := ledger.NewBorrowedStorageLedgerRepository(store)
	seed.SetTimeSource(func() time.Time { return createdAt })
	if err := seed.CreateRun(ctx, "K", ledger.RunSnapshot{
		RunID: "run-abandoned", Status: ledger.RunStatusCreated, CreatedAt: createdAt,
	}); err != nil {
		t.Fatalf("seed abandoned run: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed repo: %v", err)
	}
	clock := func() time.Time { return createdAt }
	makeCoord := func() *coordinator {
		repo := ledger.NewStorageLedgerRepository(store)
		repo.SetTimeSource(clock)
		d := runtime.New(runtime.Policy{})
		var handler runtime.Handler = staticHandler{out: json.RawMessage(`{"ok":true}`)}
		if invoked != nil {
			handler = invoker(func(context.Context, runtime.Request) (json.RawMessage, error) {
				invoked()
				return json.RawMessage(`{"ok":true}`), nil
			})
		}
		if err := d.Register(runtime.Subagent, "worker", handler); err != nil {
			t.Fatal(err)
		}
		return New(repo, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)
	}
	return makeCoord(), makeCoord(), makeCoord
}

// TestConcurrentSpawnSameKeyDoesNotDoubleExecute pins R4-1: with two processes
// A and B spawning the same key K over an abandoned run X, only the process
// that actually reclaims X may proceed to create; everyone else must back off
// (ErrIdempotencyKeyContended) and converge onto the winner's run. Pre-fix, B
// either fell through to create (a second run / double execution) or returned a
// dead 'create run: duplicate' error - never a handle to A's run.
//
// The test drives the ledger directly so the race is deterministic: A's reclaim
// is simulated with the repo primitives (ClaimRun probe + DeleteRun), exactly as
// reclaimAbandonedRun performs them, and no assertion depends on wall-clock
// timing between the two processes.
func TestConcurrentSpawnSameKeyDoesNotDoubleExecute(t *testing.T) {
	ctx := context.Background()
	var invocations atomic.Int32
	a, b, freshB := concurrentSpawnCoordinators(t, time.Now().Add(-2*abandonedRunGracePeriod), func() { invocations.Add(1) })
	const key = "K"

	// Phase 1 - A is mid-reclaim: it holds the probe claim on X (the window
	// between ClaimRun and DeleteRun in reclaimAbandonedRun). B's recovery of K
	// must report contention (R4-1), NOT a 'fresh run' signal: only the
	// successful reclaimer may proceed to create.
	if err := a.repo.ClaimRun(ctx, "run-abandoned", a.holderID); err != nil {
		t.Fatalf("A claim probe on X: %v", err)
	}
	if h, found, err := b.recoverByIdempotencyKey(ctx, key, "fingerprint"); !errors.Is(err, ErrIdempotencyKeyContended) {
		t.Fatalf("B recovery of K while A holds the reclaim probe: err=%v (handle=%v found=%v), want %v so B backs off and never creates a second run", err, h, found, ErrIdempotencyKeyContended)
	}
	if err := a.repo.DeleteRun(ctx, "run-abandoned"); err != nil {
		t.Fatalf("A DeleteRun(X): %v", err)
	}

	// Phase 2 - A completes its reclaim-then-create: a fresh coordinator over
	// the same store creates Y_A under K and executes the work to completion.
	hA, err := a.Spawn(ctx, []subagents.Task{idempotencyTask()}, key)
	if err != nil {
		t.Fatalf("A Spawn(K): %v", err)
	}
	joinCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := a.Join(joinCtx, hA); err != nil {
		t.Fatalf("A Join(Y_A): %v", err)
	}

	// Phase 3 - a FRESH B's Spawn must converge onto A's run (dedup), never
	// create Y_B. After A's run is durably visible, B's Spawn (fresh coordinator
	// over the same store, per the R4-1 recipe) returns a handle to Y_A and the
	// keyed work has executed exactly once.
	b2 := freshB()
	hB, err := b2.Spawn(ctx, []subagents.Task{idempotencyTask()}, key)
	if err != nil {
		t.Fatalf("B Spawn(K) after A's run is visible: %v", err)
	}
	if hB.runID != hA.runID {
		t.Fatalf("double execution: B spawned run %q under K, want A's run %q; the loser must dedup onto the winner", hB.runID, hA.runID)
	}
	if _, err := b2.Join(joinCtx, hB); err != nil {
		t.Fatalf("B Join(Y_A): %v", err)
	}
	if n := invocations.Load(); n != 1 {
		t.Fatalf("worker invocations = %d, want exactly 1 (the keyed work must never execute twice)", n)
	}
}

// TestYoungAbandonedRunReturnsContentionNotCreate pins the R4-1 young-run
// branch: a keyed run in 'created + zero tasks' younger than the grace period
// is a live creator mid-creation, never abandoned. B's Spawn must NOT create a
// second run: it surfaces ErrIdempotencyKeyContended after the bounded retry
// budget (or dedups once the creator's run becomes visible) - never a fresh
// run, and never a dead handle onto the young row.
func TestYoungAbandonedRunReturnsContentionNotCreate(t *testing.T) {
	ctx := context.Background()
	var invocations atomic.Int32
	b, _, _ := concurrentSpawnCoordinators(t, time.Now(), func() { invocations.Add(1) })
	const key = "K"

	h, err := b.Spawn(ctx, []subagents.Task{idempotencyTask()}, key)
	if !errors.Is(err, ErrIdempotencyKeyContended) {
		if h != nil {
			t.Fatalf("Spawn created run %q under K over a young 'created + zero tasks' run: want contention, not a second run", h.runID)
		}
		t.Fatalf("Spawn over a young abandoned run: err=%v, want %v (a live creator holds K mid-creation; only the creator may proceed)", err, ErrIdempotencyKeyContended)
	}

	// The young row must be untouched and no second run may exist.
	snap, err := b.repo.GetRunByIdempotencyKey(ctx, key)
	if err != nil {
		t.Fatalf("GetRunByIdempotencyKey(K) after contended Spawn: %v", err)
	}
	if snap.RunID != "run-abandoned" {
		t.Fatalf("keyed run = %q, want the young seeded run %q (no second run created)", snap.RunID, "run-abandoned")
	}
	if n := invocations.Load(); n != 0 {
		t.Fatalf("worker invocations = %d, want 0: the young run must not be reclaimed or executed", n)
	}
}
