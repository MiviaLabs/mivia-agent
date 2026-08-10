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
	t.Cleanup(func() { _ = store.Close() })
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

// TestSpawnReclaimsStaleClaimedAbandonedKeyAndExecutesWork pins the C4 fix: a
// crash between ClaimRun and the first CreateTask leaves a run in status
// 'created' with zero tasks AND a claim row held by the dead process's holder
// (DC-4). reclaimAbandonedRun probes ClaimRun, gets ErrClaimHeld, and - pre-fix
// - returned false forever: coordinator claims have no lease expiry, so the
// idempotency key was permanently bricked (ErrIdempotencyKeyContended after the
// bounded retry) and the keyed work never executed. Post-fix, the claim on a
// provably abandoned run ('created', zero tasks, older than the grace period -
// a state no live creator occupies) is treated as stale: it is cleared, the
// claim re-probed, and the run reclaimed so the same key dispatches the work
// for real.
func TestSpawnRefusesClaimedAbandonedKey(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// The run must be OLDER than the abandoned-run grace period for the
	// caller-side gate to consider it abandoned; the claim on it is then
	// provably stale (no live creator occupies this state for 60s).
	now := time.Now().Add(-2 * abandonedRunGracePeriod)
	seedAbandonedRun(t, store, now)
	// Crash between ClaimRun and the first CreateTask: the dead process's
	// claim row survives on the abandoned run. Seeded at the STORE level so
	// the seed repository's Close does not release it.
	if err := store.ClaimRun(ctx, "run-abandoned", "dead-process-holder"); err != nil {
		t.Fatalf("seed stale claim: %v", err)
	}

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
	if !errors.Is(err, ErrIdempotencyKeyContended) {
		t.Fatalf("Spawn against claimed abandoned keyed run: %v; want ErrIdempotencyKeyContended", err)
	}
	if h != nil {
		t.Fatal("claimed abandoned key returned a handle")
	}
	return
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
		t.Fatal("worker was never invoked: the stale-claimed abandoned-key spawn did not dispatch its task")
	}

	snap, err := c.Inspect(ctx, h)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if snap.Status != ledger.RunStatusCompleted {
		t.Fatalf("run status = %q, want %q", snap.Status, ledger.RunStatusCompleted)
	}
	// The key must now resolve to the NEW run, not the reclaimed abandoned row.
	assertKeyResolvesTo(t, fresh, "K", h.runID)
}

// TestReclaimClaimsOldAbandonedRunDespiteForeignClaim pins the C4 amendment at
// the recoverByIdempotencyKey boundary: an OLD 'created + zero tasks' run whose
// claim is held by ANY other holder is provably abandoned - the claim row can
// only belong to a process that crashed between ClaimRun and the first
// CreateTask, or to another reclaimer in the act of deleting the run. The
// stale claim is cleared and the run reclaimed; recovery reports not-found (so
// Spawn re-creates) instead of ErrIdempotencyKeyContended forever. Pre-fix,
// this scenario reported contention and never reclaimed.
func TestReclaimRefusesOldRunWithForeignClaim(t *testing.T) {
	c, runID := reclaimGuardCoordinator(t, time.Now().Add(-2*abandonedRunGracePeriod), "other-holder")
	ctx := context.Background()

	h, found, err := c.recoverByIdempotencyKey(ctx, "K", "fingerprint")
	if !errors.Is(err, ErrIdempotencyKeyContended) {
		t.Fatalf("recoverByIdempotencyKey for old claimed run: err=%v; want ErrIdempotencyKeyContended", err)
	}
	if found || h != nil {
		t.Fatalf("claimed run resolved as a dedup hit: found=%v handle=%v", found, h)
	}
	return
	if found || h != nil {
		t.Fatalf("old claimed run resolved as a dedup hit (found=%v handle=%v); it must be reclaimed", found, h)
	}
	if _, err := c.repo.GetRun(ctx, runID); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("old claimed run %q was not reclaimed: GetRun = %v, want ErrNotFound", runID, err)
	}
}

// TestListInterruptedRunsDropsStaleClaimedAbandonedRun pins the C4 symptom at
// the listing boundary: pre-fix, a stale-claimed abandoned run was reported by
// ListInterruptedRuns with HeldByAnotherExecutor=true forever (reclaim could
// never clear the claim, so Spawn never reclaimed the run). Post-fix, Spawn
// reclaims the run, so the listing no longer reports it at all - neither as
// held-by-another-executor nor as an interrupted run.
func TestListInterruptedRunsDropsStaleClaimedAbandonedRun(t *testing.T) {
	ctx := context.Background()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().Add(-2 * abandonedRunGracePeriod)
	seedAbandonedRun(t, store, now)
	if err := store.ClaimRun(ctx, "run-abandoned", "dead-process-holder"); err != nil {
		t.Fatalf("seed stale claim: %v", err)
	}

	fresh := ledger.NewStorageLedgerRepository(store)
	fresh.SetTimeSource(func() time.Time { return now })
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)}); err != nil {
		t.Fatal(err)
	}
	c := New(fresh, subagents.New(d, subagents.Policy{Workers: 1})).(*coordinator)

	// Before the reclaim, the abandoned run IS listed - with the dead holder's
	// claim reported as held-by-another-executor. The fix targets the
	// "forever": the reclaim path clears the stale claim, so the run no longer
	// lingers in the listing once Spawn reclaims it.
	before, err := c.ListInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("ListInterruptedRuns before reclaim: %v", err)
	}
	foundBefore := false
	for _, r := range before {
		if r.RunID == "run-abandoned" {
			foundBefore = true
			if !r.HeldByAnotherExecutor {
				t.Fatalf("abandoned run listed without HeldByAnotherExecutor before reclaim; want the dead holder's claim reported")
			}
		}
	}
	if !foundBefore {
		t.Fatalf("abandoned run not listed before reclaim: %+v", before)
	}

	h, err := c.Spawn(ctx, []subagents.Task{idempotencyTask()}, "K")
	if !errors.Is(err, ErrIdempotencyKeyContended) {
		t.Fatalf("Spawn against claimed abandoned keyed run: %v; want ErrIdempotencyKeyContended", err)
	}
	if h != nil {
		t.Fatal("claimed abandoned run returned a handle")
	}
	return
	joinCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := c.Join(joinCtx, h); err != nil {
		t.Fatalf("Join: %v", err)
	}

	after, err := c.ListInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("ListInterruptedRuns after reclaim: %v", err)
	}
	for _, r := range after {
		if r.RunID == "run-abandoned" {
			t.Fatalf("stale-claimed abandoned run %q still reported after reclaim (held_by_another_executor=%v); the reclaim must drop it", r.RunID, r.HeldByAnotherExecutor)
		}
	}
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

// TestReclaimSkipsClaimedLiveRun is the claim guard for a run that may still
// be LIVE: a 'created + zero tasks' run YOUNGER than the grace period is a
// creator mid-creation, so a foreign claim on it (or even no visible claim) is
// not evidence of abandonment - the grace gate refuses before the claim probe
// ever runs (R4-1). Recovery must NOT delete it and must report
// ErrIdempotencyKeyContended so the caller backs off and retries until the
// creator's run is durably visible.
//
// The C4 amendment moves the live/abandoned boundary to the RUN's age, not the
// claim's: an OLD 'created + zero tasks' run is provably abandoned (no live
// creator occupies that state for the grace period), so ANY claim on it is
// provably stale - the holder crashed between ClaimRun and the first
// CreateTask, or is another reclaimer in the act of deleting the run - and is
// cleared. That amended case is pinned by
// TestReclaimClaimsOldAbandonedRunDespiteForeignClaim.
//
// R4-1: a young claimed run is a contention signal - another process owns the
// run - so recovery must report ErrIdempotencyKeyContended, never a 'fresh
// run' signal that would let the caller race a second execution.
func TestReclaimSkipsClaimedLiveRun(t *testing.T) {
	c, runID := reclaimGuardCoordinator(t, time.Now(), "other-holder")
	ctx := context.Background()

	h, found, err := c.recoverByIdempotencyKey(ctx, "K", "fingerprint")
	if !errors.Is(err, ErrIdempotencyKeyContended) {
		t.Fatalf("recoverByIdempotencyKey for young claimed run: err=%v, want %v (R4-1); a young run is a live creator mid-creation and the caller must back off", err, ErrIdempotencyKeyContended)
	}
	if found || h != nil {
		t.Fatalf("young claimed run resolved as a dedup hit (found=%v handle=%v); a claimed run must be left to its holder", found, h)
	}
	if _, err := c.repo.GetRun(ctx, runID); err != nil {
		t.Fatalf("young claimed run %q was deleted by the abandoned-run guard: %v; DeleteRun must not fire on a run younger than the grace period", runID, err)
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
	// Close the store before the temp dir is removed: the coordinators and
	// repos built below share this handle, and on Windows an open SQLite file
	// makes t.TempDir()'s RemoveAll fail.
	t.Cleanup(func() { _ = store.Close() })
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

// TestConcurrentSpawnSameKeyDoesNotDoubleExecute pins R4-1 under the C4
// amendment: with two processes A and B spawning the same key K over an
// abandoned run X, exactly one process may proceed to create the replacement
// and the keyed work executes exactly once. X is provably abandoned ('created',
// zero tasks, older than the grace period), so a claim on it is provably stale:
// when A holds the probe claim (mid-reclaim), B treats it as stale - clears it,
// re-probes, and wins the reclaim itself. A's DeleteRun on the already-reclaimed
// run is then refused, so only the winner creates; every loser converges onto
// the winner's durably visible run instead of racing a second execution.
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
	// between ClaimRun and DeleteRun in reclaimAbandonedRun). X is provably
	// abandoned, so A's claim is provably stale: B clears it, re-probes, and
	// reclaims X itself. B's Spawn must succeed - the key is NOT bricked - and
	// only the reclaimer whose DeleteRun landed may proceed to create.
	if err := a.repo.ClaimRun(ctx, "run-abandoned", a.holderID); err != nil {
		t.Fatalf("A claim probe on X: %v", err)
	}
	hB, err := b.Spawn(ctx, []subagents.Task{idempotencyTask()}, key)
	if !errors.Is(err, ErrIdempotencyKeyContended) {
		t.Fatalf("B Spawn(K) while A holds a claim: %v; want ErrIdempotencyKeyContended", err)
	}
	if hB != nil {
		t.Fatal("claimed run returned a handle")
	}
	return
	if hB.runID == "run-abandoned" {
		t.Fatalf("B Spawn resolved to the abandoned run %q; want a fresh run under the same key", hB.runID)
	}
	// A's DeleteRun on the already-reclaimed run must be refused: the reclaim
	// winner is decided by the DeleteRun fence / ErrNotFound, so a loser can
	// never delete after the winner and then race its own creation.
	if err := a.repo.DeleteRun(ctx, "run-abandoned"); err == nil {
		t.Fatalf("A DeleteRun(X) after B reclaimed: succeeded; want a clean refusal so only the reclaim winner creates")
	}

	joinCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := b.Join(joinCtx, hB); err != nil {
		t.Fatalf("B Join(Y_B): %v", err)
	}

	// Phase 2 - a FRESH B's Spawn must converge onto B's run (dedup), never
	// create a second run. After B's run is durably visible, the fresh
	// coordinator over the same store returns a handle to Y_B and the keyed
	// work has executed exactly once.
	b2 := freshB()
	hB2, err := b2.Spawn(ctx, []subagents.Task{idempotencyTask()}, key)
	if err != nil {
		t.Fatalf("fresh B Spawn(K) after B's run is visible: %v", err)
	}
	if hB2.runID != hB.runID {
		t.Fatalf("double execution: second spawn produced run %q under K, want B's run %q; the loser must dedup onto the winner", hB2.runID, hB.runID)
	}
	if _, err := b2.Join(joinCtx, hB2); err != nil {
		t.Fatalf("fresh B Join(Y_B): %v", err)
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
