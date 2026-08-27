package ledger

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// Repository contract suite (RED phase). Production storage.go is stubbed, so
// every test here must COMPILE and FAIL on assertions — it pins down the exact
// observable contract the storage implementation must satisfy. Each behavioral
// test is table-driven over both backends (memory and sqlite) unless noted.

// fixedClock is the deterministic time source shared by both backends. Every
// repository under test stamps StartedAt/FinishedAt/ResolvedAt from this
// clock, so assertions compare against exact instants, never wall-clock
// ranges.
var fixedClock = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func nowFixed() time.Time { return fixedClock }

// requireErr asserts err equals want (via errors.Is, so wrapped sentinels
// count). want == nil asserts success.
func requireErr(t *testing.T, err, want error, msg string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", msg, err, want)
	}
}

// runID mints a per-test run ID: wfr- prefix + the (unique) test name, plus
// any optional disambiguating parts. Run IDs must be distinct across tests.
func runID(t *testing.T, parts ...string) string {
	t.Helper()
	id := "wfr-" + t.Name()
	for _, p := range parts {
		id += "-" + p
	}
	return id
}

// makeSnapshotJSON builds the canonical snapshot JSON via MarshalSnapshot.
func makeSnapshotJSON(t *testing.T) []byte {
	t.Helper()
	data, err := MarshalSnapshot(Snapshot{
		SchemaVersion:    1,
		DefinitionTOML:   []byte("x"),
		DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return data
}

// newRun builds a valid pending run snapshot plus its canonical snapshot JSON.
func newRun(t *testing.T, run string) (RunSnapshot, []byte) {
	t.Helper()
	return RunSnapshot{
		RunID:        run,
		WorkflowName: "test-wf",
		Status:       RunStatusPending,
		ActiveStepID: "start",
	}, makeSnapshotJSON(t)
}

// newMemoryRepo returns a repository over a fresh in-memory store.
func newMemoryRepo(t *testing.T) *StorageRepository {
	t.Helper()
	r := NewMemoryRepository()
	r.SetTimeSource(nowFixed)
	return r
}

// openSQLiteRepo opens a fresh SQLite store in a temp dir and wraps it in a
// repository. It returns the repo, the borrowed store, the db file path (for
// reopen tests) and a cleanup func.
func openSQLiteRepo(t *testing.T) (*StorageRepository, storage.Store, string, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wf.db")
	store, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	r := NewStorageRepository(store)
	r.SetTimeSource(nowFixed)
	return r, store, path, func() {
		_ = r.Close()
		_ = store.Close()
	}
}

// newSQLiteRepo returns a repository over a fresh SQLite store plus cleanup.
func newSQLiteRepo(t *testing.T) (*StorageRepository, func()) {
	t.Helper()
	r, _, _, done := openSQLiteRepo(t)
	return r, done
}

// repos returns one repository per backend for table-driven tests.
func repos(t *testing.T) map[string]*StorageRepository {
	t.Helper()
	out := map[string]*StorageRepository{"memory": newMemoryRepo(t)}
	r, done := newSQLiteRepo(t)
	t.Cleanup(done)
	out["sqlite"] = r
	return out
}

// repoPair builds two repository instances that share ONE underlying store:
// the memory pair wraps a single storage.Memory instance; the sqlite pair
// opens two connections to the same file. Catch-up, claims and recovery must
// cross repository instances over the shared store.
type repoPair struct {
	name string
	new  func(t *testing.T) (repoA, repoB *StorageRepository, done func())
}

func newMemoryPair(t *testing.T) (*StorageRepository, *StorageRepository, func()) {
	t.Helper()
	store := storage.NewMemory()
	a := NewStorageRepository(store)
	a.SetTimeSource(nowFixed)
	b := NewStorageRepository(store)
	b.SetTimeSource(nowFixed)
	return a, b, func() {}
}

func newSQLitePair(t *testing.T) (*StorageRepository, *StorageRepository, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wf.db")
	storeA, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite A: %v", err)
	}
	a := NewStorageRepository(storeA)
	a.SetTimeSource(nowFixed)
	storeB, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite B: %v", err)
	}
	b := NewStorageRepository(storeB)
	b.SetTimeSource(nowFixed)
	return a, b, func() {
		_ = storeA.Close()
		_ = storeB.Close()
	}
}

func repoPairs() []repoPair {
	return []repoPair{
		{"memory", newMemoryPair},
		{"sqlite", newSQLitePair},
	}
}

// ---------------------------------------------------------------------------
// 1. CreateRun admission
// ---------------------------------------------------------------------------

func TestStorageRepository_CreateRun(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)

			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			got, err := repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if got.RunID != run {
				t.Fatalf("GetRun.RunID = %q, want %q", got.RunID, run)
			}
			if got.WorkflowName != snap.WorkflowName {
				t.Fatalf("GetRun.WorkflowName = %q, want %q", got.WorkflowName, snap.WorkflowName)
			}
			if got.Status != RunStatusPending {
				t.Fatalf("GetRun.Status = %q, want %q", got.Status, RunStatusPending)
			}
			if got.Version != 1 {
				t.Fatalf("GetRun.Version = %d, want 1", got.Version)
			}
			if !got.StartedAt.Equal(fixedClock) {
				t.Fatalf("GetRun.StartedAt = %v, want %v (clock-stamped)", got.StartedAt, fixedClock)
			}

			// GetRunSnapshot returns the exact bytes passed at admission.
			raw, err := repo.GetRunSnapshot(ctx, run)
			if err != nil {
				t.Fatalf("GetRunSnapshot: %v", err)
			}
			if !bytes.Equal(raw, json) {
				t.Fatalf("GetRunSnapshot = %q, want the exact bytes passed %q", raw, json)
			}

			// Second CreateRun with the same run ID is a duplicate.
			requireErr(t, repo.CreateRun(ctx, snap, json), ErrDuplicate, "CreateRun duplicate run ID")
		})
	}
}

// ---------------------------------------------------------------------------
// 2. CreateRun rejects non-pending snapshots
// ---------------------------------------------------------------------------

func TestStorageRepository_CreateRunRejectsNonPendingStatus(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			snap.Status = RunStatusRunning

			requireErr(t, repo.CreateRun(ctx, snap, json), ErrInvalidTransition, "CreateRun non-pending")

			// The run must NOT exist after the rejected create.
			_, err := repo.GetRun(ctx, run)
			requireErr(t, err, ErrNotFound, "GetRun after rejected CreateRun")
		})
	}
}

// ---------------------------------------------------------------------------
// 3. ListRuns
// ---------------------------------------------------------------------------

func TestStorageRepository_ListRuns(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			// Empty store -> empty list.
			got, err := repo.ListRuns(ctx)
			if err != nil {
				t.Fatalf("ListRuns (empty): %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("ListRuns (empty) = %d runs, want 0", len(got))
			}

			runA := runID(t, "a")
			runB := runID(t, "b")
			snapA, jsonA := newRun(t, runA)
			snapB, jsonB := newRun(t, runB)
			requireErr(t, repo.CreateRun(ctx, snapA, jsonA), nil, "create run A")
			requireErr(t, repo.CreateRun(ctx, snapB, jsonB), nil, "create run B")

			// No filter -> all runs.
			got, err = repo.ListRuns(ctx)
			if err != nil {
				t.Fatalf("ListRuns: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("ListRuns = %d runs, want 2", len(got))
			}

			// Status filter -> only matching.
			got, err = repo.ListRuns(ctx, RunStatusPending)
			if err != nil {
				t.Fatalf("ListRuns(pending): %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("ListRuns(pending) = %d runs, want 2", len(got))
			}

			got, err = repo.ListRuns(ctx, RunStatusSucceeded)
			if err != nil {
				t.Fatalf("ListRuns(succeeded): %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("ListRuns(succeeded) = %d runs, want 0", len(got))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. CompareAndSetRunStatus
// ---------------------------------------------------------------------------

func TestStorageRepository_CompareAndSetRunStatus(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			fin := fixedClock.Add(90 * time.Minute)

			// Illegal edge from pending: pending -> succeeded.
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, 1, RunStatusSucceeded, &fin),
				ErrInvalidTransition, "pending->succeeded")

			// Valid edge: pending -> running bumps version 1 -> 2.
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, 1, RunStatusRunning, nil),
				nil, "pending->running")
			got, err := repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if got.Status != RunStatusRunning {
				t.Fatalf("Status = %q, want %q", got.Status, RunStatusRunning)
			}
			if got.Version != 2 {
				t.Fatalf("Version = %d, want 2", got.Version)
			}

			// Stale expected version -> ErrConflict, nothing changes.
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, 1, RunStatusFailed, nil),
				ErrConflict, "stale expectedVersion")
			got, err = repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun after conflict: %v", err)
			}
			if got.Status != RunStatusRunning {
				t.Fatalf("Status after conflict = %q, want %q (unchanged)", got.Status, RunStatusRunning)
			}
			if got.Version != 2 {
				t.Fatalf("Version after conflict = %d, want 2 (unchanged)", got.Version)
			}

			// Terminal transition records finishedAt: pointer persisted and
			// equal to the passed time.
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, 2, RunStatusSucceeded, &fin),
				nil, "running->succeeded")
			got, err = repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if got.Status != RunStatusSucceeded {
				t.Fatalf("Status = %q, want %q", got.Status, RunStatusSucceeded)
			}
			if got.FinishedAt == nil {
				t.Fatalf("FinishedAt not persisted for terminal transition")
			}
			if !got.FinishedAt.Equal(fin) {
				t.Fatalf("FinishedAt = %v, want %v", *got.FinishedAt, fin)
			}

			// Subsequent CAS on a terminal run is an invalid transition.
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, 3, RunStatusFailed, nil),
				ErrInvalidTransition, "CAS on terminal run")
		})
	}
}

// ---------------------------------------------------------------------------
// 11. Claims
// ---------------------------------------------------------------------------

func TestStorageRepository_Claims(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)

			requireErr(t, repo.ClaimRun(ctx, run, "h1"), nil, "claim by h1")
			requireErr(t, repo.ClaimRun(ctx, run, "h2"), ErrClaimHeld, "claim by h2 while h1 holds")
			requireErr(t, repo.ClaimRun(ctx, run, "h1"), nil, "same holder refresh")

			requireErr(t, repo.ReleaseRun(ctx, run, "h2"), ErrClaimNotHeld, "release by non-holder")
			requireErr(t, repo.ReleaseRun(ctx, run, "h1"), nil, "release by holder")
			requireErr(t, repo.ReleaseRun(ctx, run, "h1"), ErrClaimNotHeld, "release again")

			requireErr(t, repo.ClaimRun(ctx, run, "h2"), nil, "claim by h2 after release")
			requireErr(t, repo.ClearRunClaim(ctx, run), nil, "clear claim")
			requireErr(t, repo.ClaimRun(ctx, run, "h3"), nil, "claim after clear")
		})
	}
}

func TestStorageRepository_TakeoverRunClaim(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			requireErr(t, repo.ClaimRun(ctx, run, "old"), nil, "claim by old holder")
			requireErr(t, repo.TakeoverRunClaim(ctx, run, "new"), nil, "atomic takeover")
			requireErr(t, repo.ClaimRun(ctx, run, "old"), ErrClaimHeld, "old holder is fenced")
			requireErr(t, repo.ClaimRun(ctx, run, "new"), nil, "new holder refresh")
		})
	}
}

func TestStorageRepository_TakeoverFencesBoundOldWriter(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	old := NewStorageRepository(store)
	newer := NewStorageRepository(store)
	run := runID(t, "takeover-fence")
	if err := old.CreateRun(ctx, RunSnapshot{RunID: run, WorkflowName: "test", WorkflowDigest: "digest", ActiveStepID: "step", Status: RunStatusPending}, []byte("snapshot")); err != nil {
		t.Fatal(err)
	}
	requireErr(t, old.ClaimRun(ctx, run, "old"), nil, "claim old")
	requireErr(t, newer.TakeoverRunClaim(ctx, run, "new"), nil, "take over")
	oldCtx := ContextWithClaimHolder(ctx, "old")
	requireErr(t, old.CompareAndSetRunStatus(oldCtx, run, 1, RunStatusRunning, nil), ErrClaimHeld, "old writer is fenced")
	newCtx := ContextWithClaimHolder(ctx, "new")
	requireErr(t, newer.CompareAndSetRunStatus(newCtx, run, 1, RunStatusRunning, nil), nil, "new writer proceeds")
}

// ---------------------------------------------------------------------------
// 12. Content
// ---------------------------------------------------------------------------

func TestStorageRepository_Content(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			data := []byte("evidence payload")
			ref := sdkadapter.Mint(sdkadapter.KindOutput, data)
			if ref == "" {
				t.Fatalf("sdkadapter.Mint returned empty ref")
			}

			// Round-trip.
			requireErr(t, repo.StoreContent(ctx, ref, data), nil, "store content")
			got, err := repo.LoadContent(ctx, ref)
			if err != nil {
				t.Fatalf("LoadContent: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("LoadContent = %q, want %q", got, data)
			}

			// Unknown ref -> ErrContentNotFound.
			_, err = repo.LoadContent(ctx, "missing-ref")
			requireErr(t, err, ErrContentNotFound, "load unknown content")

			// Storing empty data is a no-op: nothing is stored.
			emptyRef := "ref:output:" + strings.Repeat("0", 64)
			requireErr(t, repo.StoreContent(ctx, emptyRef, []byte{}), nil, "store empty content")
			_, err = repo.LoadContent(ctx, emptyRef)
			requireErr(t, err, ErrContentNotFound, "load after empty store")
		})
	}
}

// TestStorageRepository_LoadContentVerifiesSha256Digest pins the Step-5 audit
// fix: LoadContent verifies sha256(data) against the ref's hex digest when the
// ref carries the "sha256:" prefix, so a bare ref lookup can no longer return
// bytes that do not hash to the ref (content corruption or ref mix-ups fail
// loudly). Other ref shapes (e.g. sdkadapter CLI "ref:output:<hex>") skip digest
// verification and load verbatim.
func TestStorageRepository_LoadContentVerifiesSha256Digest(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			data := []byte("evidence payload")
			ref := "sha256:" + DigestHex(data)
			requireErr(t, repo.StoreContent(ctx, ref, data), nil, "store content under sha256 ref")

			// Matching digest: LoadContent succeeds.
			got, err := repo.LoadContent(ctx, ref)
			if err != nil {
				t.Fatalf("LoadContent with matching digest: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("LoadContent = %q, want %q", got, data)
			}

			// Corrupted bytes under a sha256 ref: the digest no longer matches,
			// so LoadContent must fail with a digest-mismatch error instead of
			// returning the bytes. The ref claims a digest for bytes that were
			// NOT stored under it, so the lookup succeeds but the verification
			// must reject the mismatched content.
			corrupt := []byte("corrupted bytes")
			claimed := []byte("the bytes this ref claims to address")
			corruptRef := "sha256:" + DigestHex(claimed) // ref names a digest that corrupt does NOT match
			requireErr(t, repo.StoreContent(ctx, corruptRef, corrupt), nil, "store corrupted content under a mismatched sha256 ref")
			_, err = repo.LoadContent(ctx, corruptRef)
			if err == nil {
				t.Fatal("LoadContent of corrupted bytes under a sha256 ref must error")
			}
			if !strings.Contains(err.Error(), "digest mismatch") {
				t.Fatalf("corruption error = %v, want a digest-mismatch message", err)
			}

			// Non-sha256 ref shapes (CLI) skip digest verification: bytes
			// that do not hash to the ref's hex still load verbatim.
			plainRef := "ref:output:" + strings.Repeat("0", 64)
			requireErr(t, repo.StoreContent(ctx, plainRef, data), nil, "store content under a CLI ref shape")
			got, err = repo.LoadContent(ctx, plainRef)
			if err != nil {
				t.Fatalf("LoadContent of a non-sha256 ref must skip verification: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("LoadContent = %q, want %q", got, data)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 13. Close
// ---------------------------------------------------------------------------

func TestStorageRepository_Close(t *testing.T) {
	ctx := context.Background()

	t.Run("memory", func(t *testing.T) {
		repo := newMemoryRepo(t)
		run := runID(t)
		snap, json := newRun(t, run)
		requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
		requireErr(t, repo.Close(), nil, "Close")

		_, err := repo.GetRun(ctx, run)
		requireErr(t, err, ErrClosed, "GetRun after Close")
		requireErr(t, repo.CreateRun(ctx, snap, json), ErrClosed, "CreateRun after Close")
	})

	t.Run("sqlite", func(t *testing.T) {
		repo, _, path, done := openSQLiteRepo(t)
		defer done()
		run := runID(t)
		snap, json := newRun(t, run)
		requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
		requireErr(t, repo.Close(), nil, "Close")

		_, err := repo.GetRun(ctx, run)
		requireErr(t, err, ErrClosed, "GetRun after Close")
		requireErr(t, repo.CreateRun(ctx, snap, json), ErrClosed, "CreateRun after Close")

		// Close must NOT have closed the borrowed store: a second repository
		// over the same file still reads the run.
		store2, err := storage.OpenSQLite(path)
		if err != nil {
			t.Fatalf("reopen sqlite after repo Close: %v", err)
		}
		defer store2.Close()
		repo2 := NewStorageRepository(store2)
		repo2.SetTimeSource(nowFixed)

		got, err := repo2.GetRun(ctx, run)
		if err != nil {
			t.Fatalf("repo2.GetRun after repo.Close: %v", err)
		}
		if got.RunID != run {
			t.Fatalf("repo2.GetRun.RunID = %q, want %q (underlying store survived Close)", got.RunID, run)
		}
	})
}

// ---------------------------------------------------------------------------
// 14. Catch-up / convergence (sqlite only)
// ---------------------------------------------------------------------------

func TestStorageRepository_CatchUpAcrossRepositories(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wf.db")

	storeA, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite A: %v", err)
	}
	defer storeA.Close()
	repoA := NewStorageRepository(storeA)
	repoA.SetTimeSource(nowFixed)

	run := runID(t)
	snap, json := newRun(t, run)
	requireErr(t, repoA.CreateRun(ctx, snap, json), nil, "CreateRun")

	att := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
	requireErr(t, repoA.CreateStepAttempt(ctx, att), nil, "create attempt")

	evidence := []byte("evidence")
	outcome := AttemptOutcome{
		Status:          AttemptStatusSucceeded,
		OutputRef:       sdkadapter.Mint(sdkadapter.KindOutput, evidence),
		OutputDigest:    DigestHex(evidence),
		ToStepID:        "implement",
		TransitionIndex: 0,
		MatchDigest:     "m",
		DecisionJSON:    []byte(`{"verdict":"approved"}`),
	}
	requireErr(t, repoA.CompleteStepAttempt(ctx, run, "att-1", 1, outcome), nil, "complete attempt")

	// A second repository over the same file sees the identical state.
	storeB, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite B: %v", err)
	}
	defer storeB.Close()
	repoB := NewStorageRepository(storeB)
	repoB.SetTimeSource(nowFixed)

	got, err := repoB.GetRun(ctx, run)
	if err != nil {
		t.Fatalf("repoB.GetRun: %v", err)
	}
	if got.RunID != run || got.Status != RunStatusPending || got.Version != 1 {
		t.Fatalf("repoB run = (%q, %q, v%d), want (%q, pending, v1)", got.RunID, got.Status, got.Version, run)
	}
	if !got.StartedAt.Equal(fixedClock) {
		t.Fatalf("repoB run StartedAt = %v, want %v", got.StartedAt, fixedClock)
	}

	attempts, err := repoB.ListStepAttempts(ctx, run)
	if err != nil {
		t.Fatalf("repoB.ListStepAttempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("repoB attempts = %d, want 1", len(attempts))
	}
	ab := attempts[0]
	if ab.AttemptID != "att-1" || ab.Status != AttemptStatusSucceeded || ab.Version != 2 {
		t.Fatalf("repoB attempt = (%q, %q, v%d), want (att-1, succeeded, v2)", ab.AttemptID, ab.Status, ab.Version)
	}
	if ab.ToStepID != "implement" || ab.OutputRef != outcome.OutputRef || ab.OutputDigest != outcome.OutputDigest {
		t.Fatalf("repoB attempt route/output mismatch: %+v", ab)
	}
	if ab.FinishedAt == nil || !ab.FinishedAt.Equal(fixedClock) {
		t.Fatalf("repoB attempt FinishedAt = %v, want %v", ab.FinishedAt, fixedClock)
	}

	trans, err := repoB.ListTransitions(ctx, run)
	if err != nil {
		t.Fatalf("repoB.ListTransitions: %v", err)
	}
	if len(trans) != 1 {
		t.Fatalf("repoB transitions = %d, want 1", len(trans))
	}
	if trans[0].FromAttemptID != "att-1" || trans[0].ToStepID != "implement" {
		t.Fatalf("repoB transition = %+v, want {att-1 -> implement}", trans[0])
	}
}

// ---------------------------------------------------------------------------
// 16. SetTimeSource determinism
// ---------------------------------------------------------------------------

func TestStorageRepository_TimeSourceDeterminism(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			now := fixedClock
			repo.SetTimeSource(func() time.Time { return now })

			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			got, err := repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			if !got.StartedAt.Equal(fixedClock) {
				t.Fatalf("StartedAt = %v, want %v (fixed clock)", got.StartedAt, fixedClock)
			}

			// Advance the clock; the terminal CAS stamps FinishedAt from the
			// CURRENT clock, not from admission time.
			now = now.Add(20 * time.Minute)
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, 1, RunStatusRunning, nil),
				nil, "pending->running")

			now = now.Add(40 * time.Minute)
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, 2, RunStatusSucceeded, nil),
				nil, "running->succeeded")

			got, err = repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun: %v", err)
			}
			wantFin := fixedClock.Add(1 * time.Hour)
			if got.FinishedAt == nil {
				t.Fatalf("FinishedAt not stamped by terminal CAS")
			}
			if !got.FinishedAt.Equal(wantFin) {
				t.Fatalf("FinishedAt = %v, want %v (advanced clock)", *got.FinishedAt, wantFin)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 17. Two repositories, one store instance
// ---------------------------------------------------------------------------

func TestStorageRepository_TwoRepositoriesOneStore(t *testing.T) {
	ctx := context.Background()
	for _, p := range repoPairs() {
		t.Run(p.name, func(t *testing.T) {
			repo1, repo2, done := p.new(t)
			defer done()

			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo1.CreateRun(ctx, snap, json), nil, "CreateRun via repo1")

			// repo2 sees repo1's write immediately (same store instance).
			got, err := repo2.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("repo2.GetRun: %v", err)
			}
			if got.RunID != run {
				t.Fatalf("repo2.GetRun.RunID = %q, want %q", got.RunID, run)
			}
			if !got.StartedAt.Equal(fixedClock) {
				t.Fatalf("repo2 run StartedAt = %v, want %v", got.StartedAt, fixedClock)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 18. RunSnapshot.RemoteURL round-trip and rebuild survival
// ---------------------------------------------------------------------------

func TestStorageRepository_RunSnapshotRemoteURL(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryRepo(t)

	run := runID(t)
	snap, json := newRun(t, run)
	snap.RemoteURL = "https://example.com/mivia/workflows.git"
	requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

	got, err := repo.GetRun(ctx, run)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.RemoteURL != snap.RemoteURL {
		t.Fatalf("GetRun.RemoteURL = %q, want %q (CreateRun/GetRun round-trip)", got.RemoteURL, snap.RemoteURL)
	}

	// RemoteURL survives a projection rebuild: the store holds the
	// wf_run_created event carrying the field, and RebuildProjection replays
	// it into the snapshot.
	events, err := repo.store.Events(ctx, run)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	proj, err := RebuildProjection(events)
	if err != nil {
		t.Fatalf("RebuildProjection: %v", err)
	}
	rebuilt := requireRun(t, proj)
	if rebuilt.RemoteURL != snap.RemoteURL {
		t.Fatalf("rebuilt RemoteURL = %q, want %q (rebuild survival)", rebuilt.RemoteURL, snap.RemoteURL)
	}
}

// errAppendSentinel is the sentinel error the failingStore returns from Append.
var errAppendSentinel = errors.New("sentinel append failure")

// failingStore wraps a storage.Store and fails Append while fail is set; all
// other operations delegate to the wrapped store.
type failingStore struct {
	storage.Store
	fail bool
}

func (f *failingStore) Append(ctx context.Context, e storage.Event) error {
	if f.fail {
		return errAppendSentinel
	}
	return f.Store.Append(ctx, e)
}

func (f *failingStore) AppendClaimed(ctx context.Context, e storage.Event, holder string) error {
	if f.fail {
		return errAppendSentinel
	}
	return f.Store.AppendClaimed(ctx, e, holder)
}

// countingStore wraps a storage.Store and counts Events(runID) calls, so a
// regression to the O(N^2) foreign-run catch-up shows up as a non-zero count.
// It is per-test: the test resets the counter before the call under
// observation and reads it after.
type countingStore struct {
	storage.Store
	eventsCalls int
}

func (c *countingStore) Events(ctx context.Context, runID string) ([]storage.Event, error) {
	c.eventsCalls++
	return c.Store.Events(ctx, runID)
}
