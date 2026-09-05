package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// ---------------------------------------------------------------------------
// 19. Append failure rolls back the in-memory projection
// ---------------------------------------------------------------------------
func TestStorageRepository_AppendFailureRollsBackProjection(t *testing.T) {
	ctx := context.Background()
	newWrapped := map[string]func(t *testing.T) (*StorageRepository, *failingStore, func()){
		"memory": func(t *testing.T) (*StorageRepository, *failingStore, func()) {
			f := &failingStore{Store: storage.NewMemory()}
			r := NewStorageRepository(f)
			r.SetTimeSource(nowFixed)
			return r, f, func() {}
		},
		"sqlite": func(t *testing.T) (*StorageRepository, *failingStore, func()) {
			store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "wf.db"))
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			f := &failingStore{Store: store}
			r := NewStorageRepository(f)
			r.SetTimeSource(nowFixed)
			return r, f, func() { _ = store.Close() }
		},
	}

	for name, newRepo := range newWrapped {
		t.Run(name, func(t *testing.T) {
			repo, f, done := newRepo(t)
			defer done()

			run := runID(t)
			snap, json := newRun(t, run)
			snap.ActiveStepID = "plan"
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			// Arm the failure: the next append must fail with the sentinel.
			f.fail = true
			a1 := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
			err := repo.CreateStepAttempt(ctx, a1)
			if !errors.Is(err, errAppendSentinel) {
				t.Fatalf("CreateStepAttempt with failing store: err = %v, want sentinel", err)
			}

			// The in-memory projection rolled back to store state: no attempt
			// is visible and the derived active step is unchanged.
			_, err = repo.GetStepAttempt(ctx, run, "att-1")
			requireErr(t, err, ErrNotFound, "GetStepAttempt after failed append")

			got, err := repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun after failed append: %v", err)
			}
			if got.ActiveStepID != "plan" {
				t.Fatalf("GetRun.ActiveStepID = %q after failed append, want %q (projection rolled back)",
					got.ActiveStepID, "plan")
			}

			// Disarm: the same create now succeeds and is durable.
			f.fail = false
			requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "CreateStepAttempt after disarm")
			gotA, err := repo.GetStepAttempt(ctx, run, "att-1")
			if err != nil {
				t.Fatalf("GetStepAttempt after retry: %v", err)
			}
			if gotA.Status != AttemptStatusRunning || gotA.Version != 1 {
				t.Fatalf("retried attempt = (%q, v%d), want (running, v1)", gotA.Status, gotA.Version)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 20. Store id PRIMARY KEY backstops the deterministic event ID
// ---------------------------------------------------------------------------

// TestStorageRepository_DuplicateAppendByteCompare pins the store's id
// PRIMARY KEY for a re-append of the same deterministic event ID with a
// byte-identical payload: the append is rejected and the log still holds
// exactly one event with the original payload.
func TestStorageRepository_DuplicateAppendByteCompare(t *testing.T) {
	ctx := context.Background()

	// The byte-compare branch of appendEvent (identical payload -> idempotent
	// nil, different payload -> ErrConflict) is only reachable when two
	// writers race the same deterministic event ID before either catches up,
	// which is not deterministically reachable through the public API. The
	// honest deterministic pin is the mechanism underneath: the store's id
	// PRIMARY KEY rejects a second append of the same ID, and the event log
	// still holds exactly one event, whatever the payload bytes.
	//
	// A FRESH run per test keeps the audit trail clean.
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "wf.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()
	repo := NewStorageRepository(store)
	repo.SetTimeSource(nowFixed)

	run := runID(t)
	snap, json := newRun(t, run)
	requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
	a1 := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
	requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "create attempt")

	// Locate the wf_attempt_started event the repo appended.
	events, err := store.Events(ctx, run)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	var original storage.Event
	found := false
	for _, e := range events {
		if e.Kind == eventKindAttemptStarted {
			original = e
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no wf_attempt_started event in store log")
	}

	// Re-append the SAME event ID directly, bypassing the repo, with a
	// byte-identical payload. The id PRIMARY KEY must reject it either way.
	dup := original
	dup.Payload = append([]byte(nil), original.Payload...)
	requireErr(t, store.Append(ctx, dup), storage.ErrDuplicate, "store.Append with same ID")

	// Exactly one event still carries that ID, with the ORIGINAL payload:
	// the foreign append never landed.
	events, err = store.Events(ctx, run)
	if err != nil {
		t.Fatalf("store.Events after duplicate append: %v", err)
	}
	count := 0
	for _, e := range events {
		if e.ID == original.ID {
			count++
			if !bytes.Equal(e.Payload, original.Payload) {
				t.Fatalf("surviving event payload differs from the original append")
			}
		}
	}
	if count != 1 {
		t.Fatalf("events with ID %q = %d, want exactly 1", original.ID, count)
	}
}

// TestStorageRepository_DuplicateAppendDifferentPayload pins the store's id
// PRIMARY KEY for a re-append of the same deterministic event ID with a
// DIFFERENT payload: the append is rejected and the log still holds exactly
// one event with the ORIGINAL payload.
func TestStorageRepository_DuplicateAppendDifferentPayload(t *testing.T) {
	ctx := context.Background()

	// The byte-compare branch of appendEvent (identical payload -> idempotent
	// nil, different payload -> ErrConflict) is only reachable when two
	// writers race the same deterministic event ID before either catches up,
	// which is not deterministically reachable through the public API. The
	// honest deterministic pin is the mechanism underneath: the store's id
	// PRIMARY KEY rejects a second append of the same ID, and the event log
	// still holds exactly one event, whatever the payload bytes.
	//
	// A FRESH run per test keeps the audit trail clean.
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "wf.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()
	repo := NewStorageRepository(store)
	repo.SetTimeSource(nowFixed)

	run := runID(t)
	snap, json := newRun(t, run)
	requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
	a1 := StepAttempt{AttemptID: "att-1", RunID: run, StepID: "plan", AttemptNo: 1}
	requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "create attempt")

	// Locate the wf_attempt_started event the repo appended.
	events, err := store.Events(ctx, run)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	var original storage.Event
	found := false
	for _, e := range events {
		if e.Kind == eventKindAttemptStarted {
			original = e
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no wf_attempt_started event in store log")
	}

	// Re-append the SAME event ID directly, bypassing the repo, with a
	// DIFFERENT payload: same logical key, different attempt ID -> different
	// bytes. The id PRIMARY KEY must reject it either way.
	alt, err := marshalAttemptStarted(attemptStartedPayload{
		Attempt:   StepAttempt{AttemptID: "att-other", RunID: original.RunID, StepID: "plan", AttemptNo: 1},
		CreatedAt: fixedClock,
	})
	if err != nil {
		t.Fatalf("marshal alternate payload: %v", err)
	}
	dup := original
	dup.Payload = alt
	requireErr(t, store.Append(ctx, dup), storage.ErrDuplicate, "store.Append with same ID")

	// Exactly one event still carries that ID, with the ORIGINAL payload:
	// the foreign append never landed.
	events, err = store.Events(ctx, run)
	if err != nil {
		t.Fatalf("store.Events after duplicate append: %v", err)
	}
	count := 0
	for _, e := range events {
		if e.ID == original.ID {
			count++
			if !bytes.Equal(e.Payload, original.Payload) {
				t.Fatalf("surviving event payload differs from the original append")
			}
		}
	}
	if count != 1 {
		t.Fatalf("events with ID %q = %d, want exactly 1", original.ID, count)
	}
}

// ---------------------------------------------------------------------------
// 21. Cross-instance duplicate triple dispatches no second attempt
// ---------------------------------------------------------------------------

func TestStorageRepository_CrossInstanceDuplicateTriple(t *testing.T) {
	ctx := context.Background()
	repoA, repoB, done := newSQLitePair(t)
	defer done()

	run := runID(t)
	snap, json := newRun(t, run)
	requireErr(t, repoA.CreateRun(ctx, snap, json), nil, "CreateRun via repoA")

	attA := StepAttempt{AttemptID: "wfa-A", RunID: run, StepID: "plan", AttemptNo: 1}
	requireErr(t, repoA.CreateStepAttempt(ctx, attA), nil, "create attempt wfa-A")

	// A second instance over the same file must not dispatch a second attempt
	// for the same (runID, stepID, attemptNo) triple: catch-up rebuilds the
	// run into repoB's projection and the in-process duplicate check fires
	// ErrDuplicate before anything is appended.
	attB := StepAttempt{AttemptID: "wfa-B", RunID: run, StepID: "plan", AttemptNo: 1}
	requireErr(t, repoB.CreateStepAttempt(ctx, attB), ErrDuplicate, "duplicate triple via repoB")

	// The store holds exactly ONE wf_attempt_started for that triple, with
	// the FIRST attempt's ID in the payload: no second dispatch happened.
	events, err := repoA.store.Events(ctx, run)
	if err != nil {
		t.Fatalf("store.Events: %v", err)
	}
	count := 0
	for _, e := range events {
		if e.Kind != eventKindAttemptStarted {
			continue
		}
		count++
		p, err := unmarshalAttemptStarted(e.Payload)
		if err != nil {
			t.Fatalf("decode wf_attempt_started payload: %v", err)
		}
		if p.Attempt.AttemptID != "wfa-A" {
			t.Fatalf("wf_attempt_started payload attempt_id = %q, want %q", p.Attempt.AttemptID, "wfa-A")
		}
	}
	if count != 1 {
		t.Fatalf("wf_attempt_started events = %d, want exactly 1", count)
	}
}

// ---------------------------------------------------------------------------
// 22. In-place mutations mirror RebuildProjection's derived active step
// ---------------------------------------------------------------------------

// TestStorageRepository_ActiveStepMatchesRebuild pins the auditor-reproduced
// ActiveStepID divergence between the LIVE projection (in-place mutations) and
// the REBUILT projection (full replay): after every mutation the live value
// must equal RebuildProjection over the raw store events, and ListRuns must
// surface the same derived step.
func TestStorageRepository_ActiveStepMatchesRebuild(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			snap.ActiveStepID = "plan"
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")

			assertActive := func(want string) {
				t.Helper()
				got, err := repo.GetRun(ctx, run)
				if err != nil {
					t.Fatalf("GetRun: %v", err)
				}
				if got.ActiveStepID != want {
					t.Fatalf("GetRun.ActiveStepID = %q, want %q", got.ActiveStepID, want)
				}
				events, err := repo.store.Events(ctx, run)
				if err != nil {
					t.Fatalf("store.Events: %v", err)
				}
				rebuilt, err := RebuildProjection(events)
				if err != nil {
					t.Fatalf("RebuildProjection: %v", err)
				}
				if got.ActiveStepID != rebuilt.ActiveStepID {
					t.Fatalf("ActiveStepID diverges: live %q, rebuild %q", got.ActiveStepID, rebuilt.ActiveStepID)
				}
				runs, err := repo.ListRuns(ctx)
				if err != nil {
					t.Fatalf("ListRuns: %v", err)
				}
				if len(runs) != 1 || runs[0].ActiveStepID != got.ActiveStepID {
					t.Fatalf("ListRuns[0].ActiveStepID = %q, want %q", runs[0].ActiveStepID, got.ActiveStepID)
				}
			}

			// 1. Initial step from the run payload.
			assertActive("plan")

			// 2. a1 starts on "plan" -> plan.
			a1 := StepAttempt{AttemptID: "a1", RunID: run, StepID: "plan", AttemptNo: 1}
			requireErr(t, repo.CreateStepAttempt(ctx, a1), nil, "create a1")
			assertActive("plan")

			// 3. a2 starts on "implement" -> implement (newest candidate).
			a2 := StepAttempt{AttemptID: "a2", RunID: run, StepID: "implement", AttemptNo: 1}
			requireErr(t, repo.CreateStepAttempt(ctx, a2), nil, "create a2")
			assertActive("implement")

			// 4. a2 completes WITH a route -> review.
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "a2", 1,
				AttemptOutcome{Status: AttemptStatusSucceeded, ToStepID: "review", TransitionIndex: 0}),
				nil, "complete a2 -> review")
			assertActive("review")

			// 5. a1 completes interrupted WITHOUT a route: the completion
			// carries no step, so the newest candidate stays "review" — the
			// live projection must NOT rewind to a1's "plan".
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "a1", 1,
				AttemptOutcome{Status: AttemptStatusInterrupted}),
				nil, "interrupt a1")
			assertActive("review")

			// 6. a3 starts with an EMPTY step: no candidate in the replay,
			// so the live projection must not wipe the derived step.
			a3 := StepAttempt{AttemptID: "a3", RunID: run, StepID: "", AttemptNo: 1}
			requireErr(t, repo.CreateStepAttempt(ctx, a3), nil, "create a3 (empty step)")
			assertActive("review")

			// 7. a3 completes interrupted: no step, nothing changes.
			requireErr(t, repo.CompleteStepAttempt(ctx, run, "a3", 1,
				AttemptOutcome{Status: AttemptStatusInterrupted}),
				nil, "interrupt a3")
			assertActive("review")
		})
	}
}

// ---------------------------------------------------------------------------
// 23. Caller-provided StartedAt survives a rebuild on a second instance
// ---------------------------------------------------------------------------

func TestStorageRepository_StartedAtSurvivesRebuild(t *testing.T) {
	ctx := context.Background()
	startedAt := fixedClock.Add(2 * time.Hour)

	for _, p := range repoPairs() {
		t.Run(p.name, func(t *testing.T) {
			repoA, repoB, done := p.new(t)
			defer done()

			run := runID(t)
			snap, json := newRun(t, run)
			snap.StartedAt = startedAt
			requireErr(t, repoA.CreateRun(ctx, snap, json), nil, "CreateRun")

			// The live projection honors the caller-provided StartedAt.
			got, err := repoA.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("repoA.GetRun: %v", err)
			}
			if !got.StartedAt.Equal(startedAt) {
				t.Fatalf("repoA StartedAt = %v, want %v", got.StartedAt, startedAt)
			}

			// A second repository instance over the SAME store (memory: same
			// store instance; sqlite: reopened file) must rebuild the same
			// StartedAt from the wf_run_created payload, not stamp CreatedAt.
			got, err = repoB.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("repoB.GetRun: %v", err)
			}
			if !got.StartedAt.Equal(startedAt) {
				t.Fatalf("repoB StartedAt = %v, want %v (payload StartedAt must survive the rebuild)", got.StartedAt, startedAt)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 25. Foreign (non-wfr-) run logs are never read during catch-up
// ---------------------------------------------------------------------------
func foreignRunRepoFactories() map[string]func(t *testing.T) (*StorageRepository, *countingStore, func()) {
	return map[string]func(t *testing.T) (*StorageRepository, *countingStore, func()){
		"memory": func(t *testing.T) (*StorageRepository, *countingStore, func()) {
			cs := &countingStore{Store: storage.NewMemory()}
			r := NewStorageRepository(cs)
			r.SetTimeSource(nowFixed)
			return r, cs, func() {}
		},
		"sqlite": func(t *testing.T) (*StorageRepository, *countingStore, func()) {
			store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "wf.db"))
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			cs := &countingStore{Store: store}
			r := NewStorageRepository(cs)
			r.SetTimeSource(nowFixed)
			return r, cs, func() { _ = store.Close() }
		},
	}
}

// appendForeignEvents writes events for a run id outside the workflow
// namespace, straight through the store, so catch-up has a foreign log to
// consider. The namespace rule says a non-"wfr-" run can never hold wf events.
func appendForeignEvents(t *testing.T, cs *countingStore, runID string, from, to int) {
	t.Helper()
	for i := from; i <= to; i++ {
		ev := storage.Event{
			ID:       fmt.Sprintf("se-%d", i),
			RunID:    runID,
			Sequence: i,
			Kind:     "run_created",
			Payload:  []byte("{}"),
		}
		if err := cs.Append(context.Background(), ev); err != nil {
			t.Fatalf("append foreign event %d: %v", i, err)
		}
	}
}

// requireNoForeignLogRead runs one GetRun and asserts the foreign run's log
// was not read while doing it.
func requireNoForeignLogRead(t *testing.T, repo *StorageRepository, cs *countingStore, wfr, when string) {
	t.Helper()
	cs.eventsCalls = 0
	if _, err := repo.GetRun(context.Background(), wfr); err != nil {
		t.Fatalf("GetRun (%s): %v", when, err)
	}
	if cs.eventsCalls != 0 {
		t.Fatalf("Events(runID) calls %s = %d, want 0 (the foreign run log must be skipped)", when, cs.eventsCalls)
	}
}

// TestStorageRepository_ForeignRunNotRebuilt pins the namespace skip on EVERY
// catch-up pass, not just the first.
//
// The guard also required Applied(runID) == 0, but skipping a run advances its
// watermark - so the next event on that foreign run made the guard fall
// through and this instance re-read and re-folded the whole foreign log. The
// store is shared with the coordinator and chat, so an active chat session
// made every workflow read pay for that session's entire history.
func TestStorageRepository_ForeignRunNotRebuilt(t *testing.T) {
	ctx := context.Background()
	const foreign = "run-foreign-1"
	for name, newRepo := range foreignRunRepoFactories() {
		t.Run(name, func(t *testing.T) {
			repo, cs, done := newRepo(t)
			defer done()

			wfr := runID(t, "wfr")
			snap, json := newRun(t, wfr)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			appendForeignEvents(t, cs, foreign, 1, 50)

			// CreateRun's own rebase read the wfr- run's (empty) log, so the
			// helper resets the counter before each pass.
			requireNoForeignLogRead(t, repo, cs, wfr, "on the first pass")

			appendForeignEvents(t, cs, foreign, 51, 51)
			requireNoForeignLogRead(t, repo, cs, wfr, "on the second pass")

			// The foreign run must not leak into the workflow view.
			runs, err := repo.ListRuns(ctx)
			if err != nil {
				t.Fatalf("ListRuns: %v", err)
			}
			for _, r := range runs {
				if r.RunID == foreign {
					t.Fatalf("ListRuns exposes foreign run %q", r.RunID)
				}
			}
		})
	}
}
