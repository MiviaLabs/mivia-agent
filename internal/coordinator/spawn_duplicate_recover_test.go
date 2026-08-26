package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// duplicateWinnerVisibleRepo models the durable uniqueness race lost by
// CreateRun: another caller's keyed run commits between our idempotency
// lookup and our own CreateRun. The first CreateRun plants that foreign
// winner in the wrapped ledger, then refuses once with ledger.ErrDuplicate,
// the way storage reports a lost race. Every later read sees the foreign
// run, so recovery can dedup onto it. No timing is involved: the race is
// pre-staged, not replayed.
type duplicateWinnerVisibleRepo struct {
	*ledger.MemoryLedgerRepository

	winner     ledger.RunSnapshot
	winnerTask ledger.TaskSnapshot
	lostRace   bool // true after one refusal; later calls pass through
}

func (r *duplicateWinnerVisibleRepo) CreateRun(ctx context.Context, key string, snapshot ledger.RunSnapshot) error {
	if r.lostRace {
		return r.MemoryLedgerRepository.CreateRun(ctx, key, snapshot)
	}
	r.lostRace = true
	// Commit the foreign winner first, then report the duplicate. This order
	// puts the winner behind every read that follows.
	if err := r.MemoryLedgerRepository.CreateRun(ctx, key, r.winner); err != nil {
		return err
	}
	if err := r.MemoryLedgerRepository.CreateTask(ctx, r.winnerTask); err != nil {
		return err
	}
	return ledger.ErrDuplicate
}

func duplicateWinnerRepo(t *testing.T) *duplicateWinnerVisibleRepo {
	t.Helper()
	now := time.Now()
	const taskID = "task-foreign"
	return &duplicateWinnerVisibleRepo{
		MemoryLedgerRepository: ledger.NewMemoryLedgerRepository(),
		winner:                 ledger.RunSnapshot{RunID: "run-duplicate-winner", Status: ledger.RunStatusRunning},
		winnerTask: ledger.TaskSnapshot{
			RunID: "run-duplicate-winner", TaskID: taskID, HandlerName: "worker",
			Status:    string(ledger.TaskStatusQueued),
			CreatedAt: now,
			Attempts: []ledger.AttemptSnapshot{{
				AttemptID: "attempt-foreign", TaskID: taskID,
				RunID: "run-duplicate-winner", AttemptNum: 1, StartedAt: now,
			}},
		},
	}
}

// TestSpawnNewReportsRecoveredDuplicateAsNotNew pins the isNew contract on
// the ErrDuplicate recovery exit of createAndStartRun. When that exit hands
// back another caller's committed run, isNew must be false. Inferring isNew
// from err == nil marked the foreign handle as freshly created, so a caller
// with its own dead wait context fired cancelOrphanedRun and killed the run
// the legitimate creator still owned.
//
// The winner's snapshot carries an empty RequestFingerprint, which recovery
// treats as fingerprint-free; a running status with one admitted task keeps
// recovery out of the contended-abandoned-run branch. Both choices keep this
// fence free of sleep and clock tuning.
func TestSpawnNewReportsRecoveredDuplicateAsNotNew(t *testing.T) {
	repo := duplicateWinnerRepo(t)
	c := newIdempotencyCoordinator(repo).(*coordinator)

	h, isNew, err := c.SpawnNew(context.Background(), []subagents.Task{idempotencyTask()}, "K")
	if err != nil {
		t.Fatalf("SpawnNew error = %v, want dedup onto the committed winner", err)
	}
	if isNew {
		t.Fatal("isNew = true for a recovered duplicate; want false so the caller never acts as owner")
	}
	if h == nil {
		t.Fatal("SpawnNew handle = nil")
	}
	if h.runID != "run-duplicate-winner" {
		t.Fatalf("handle run ID = %q, want the foreign winner %q", h.runID, "run-duplicate-winner")
	}
	if !h.recovered {
		t.Fatal("handle recovered = false for a handle recovered from the durable lookup")
	}
	if h.localActor {
		t.Fatal("recovered duplicate reports itself as the local actor; only the creator executes")
	}
	snap, err := repo.GetRunByIdempotencyKey(context.Background(), "K")
	if err != nil {
		t.Fatalf("winner vanished after dedup: GetRunByIdempotencyKey(K): %v", err)
	}
	if snap.RunID != "run-duplicate-winner" {
		t.Fatalf("key K maps to run %q after dedup, want the untouched winner", snap.RunID)
	}
}

// TestSpawnNewKeepsDuplicateErrorWhenRecoveryMisses keeps the semantics of
// TestCreateAndStartRunConsultsRecoveryOnDuplicateKey over SpawnNew: when
// recovery cannot resolve the duplicated key, the raw duplicate error still
// surfaces and no handle leaves spawnReportingNew as success.
func TestSpawnNewKeepsDuplicateErrorWhenRecoveryMisses(t *testing.T) {
	repo := &duplicateKeyedCreateRepo{MemoryLedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo).(*coordinator)

	h, isNew, err := c.SpawnNew(context.Background(), []subagents.Task{idempotencyTask()}, "K")
	if h != nil {
		t.Fatalf("SpawnNew handle = %v, want nil when recovery misses the duplicate key", h)
	}
	if isNew {
		t.Fatal("isNew = true alongside an error; want false")
	}
	if !errors.Is(err, ledger.ErrDuplicate) {
		t.Fatalf("SpawnNew error = %v, want it to wrap %v", err, ledger.ErrDuplicate)
	}
}

// TestSpawnNewSurfacesContentionAfterRetryBudget pins the bounded-retry
// contract around ErrIdempotencyKeyContended. A 'created + zero tasks' run
// born this instant is a live creator mid-run by definition, so recovery
// reports contention forever. SpawnNew retries through that fixed window
// and, once its budget is spent, surfaces the sentinel instead of creating
// over the contended key. The budget burn is part of the pinned behavior -
// the loop's interval and total are package constants, so this stays
// deterministic while costing about two seconds.
func TestSpawnNewSurfacesContentionAfterRetryBudget(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	// Commit the young creator-shaped run straight into the ledger, keyed to
	// K. Real ledger methods answer every read along the retry path.
	if err := repo.CreateRun(context.Background(), "K", ledger.RunSnapshot{
		RunID: "run-mid-creation", Status: ledger.RunStatusCreated, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	c := newIdempotencyCoordinator(repo)

	h, isNew, err := c.SpawnNew(context.Background(), []subagents.Task{idempotencyTask()}, "K")
	if h != nil {
		t.Fatalf("SpawnNew handle = %v, want nil past the retry budget", h)
	}
	if isNew {
		t.Fatal("isNew = true alongside an error; want false")
	}
	if !errors.Is(err, ErrIdempotencyKeyContended) {
		t.Fatalf("SpawnNew error = %v, want it to wrap %v", err, ErrIdempotencyKeyContended)
	}
}
