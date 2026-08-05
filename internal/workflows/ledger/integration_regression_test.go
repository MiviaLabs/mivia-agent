package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

func TestIntegrationClaimFencesWorkflowMutation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ownerStore, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open owner store: %v", err)
	}
	t.Cleanup(func() { _ = ownerStore.Close() })
	owner := NewStorageRepository(ownerStore)

	runID := "wfr-claim-fence"
	snap, snapshotJSON := newRun(t, runID)
	requireErr(t, owner.CreateRun(ctx, snap, snapshotJSON), nil, "CreateRun")
	requireErr(t, owner.ClaimRun(ctx, runID, "owner"), nil, "ClaimRun")

	otherStore, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("open other store: %v", err)
	}
	t.Cleanup(func() { _ = otherStore.Close() })
	other := NewStorageRepository(otherStore)

	run, err := other.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("other.GetRun: %v", err)
	}
	requireErr(t, other.CompareAndSetRunStatus(ctx, runID, run.Version, RunStatusRunning, nil), ErrClaimHeld, "foreign status mutation")

	stored, err := owner.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("owner.GetRun: %v", err)
	}
	if stored.Status != RunStatusPending || stored.Version != 1 {
		t.Fatalf("run after rejected mutation = %+v, want pending version 1", stored)
	}
	events, err := ownerStore.Events(ctx, runID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events after rejected mutation = %d, want 1", len(events))
	}
	requireErr(t, owner.CompareAndSetRunStatus(ctx, runID, 1, RunStatusRunning, nil), nil, "owner status mutation")
}

func TestIntegrationRejectsEmptyClaimHolder(t *testing.T) {
	ctx := context.Background()
	repo, _, _, done := openSQLiteRepo(t)
	defer done()
	runID := "wfr-empty-holder"
	snap, snapshotJSON := newRun(t, runID)
	requireErr(t, repo.CreateRun(ctx, snap, snapshotJSON), nil, "CreateRun")
	requireErr(t, repo.ClaimRun(ctx, runID, ""), ErrClaimNotHeld, "empty claim holder")
}

func TestIntegrationRejectsInvalidLedgerInputs(t *testing.T) {
	ctx := context.Background()
	repo, store, path, done := openSQLiteRepo(t)
	defer done()

	invalid, invalidJSON := newRun(t, "run-not-workflow")
	requireErr(t, repo.CreateRun(ctx, invalid, invalidJSON), ErrInvalidTransition, "non-workflow run ID")
	events, err := store.Events(ctx, invalid.RunID)
	if err != nil {
		t.Fatalf("Events for rejected run: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events for rejected run = %d, want 0", len(events))
	}

	runID := "wfr-invalid-inputs"
	snap, snapshotJSON := newRun(t, runID)
	requireErr(t, repo.CreateRun(ctx, snap, snapshotJSON), nil, "CreateRun")
	first := StepAttempt{AttemptID: "attempt-1", RunID: runID, StepID: "plan", AttemptNo: 1}
	requireErr(t, repo.CreateStepAttempt(ctx, first), nil, "first attempt")
	duplicateID := StepAttempt{AttemptID: "attempt-1", RunID: runID, StepID: "review", AttemptNo: 1}
	requireErr(t, repo.CreateStepAttempt(ctx, duplicateID), ErrDuplicate, "duplicate attempt ID")

	reopenedStore, err := storage.OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopenedStore.Close() })
	reopened := NewStorageRepository(reopenedStore)
	attempts, err := reopened.ListStepAttempts(ctx, runID)
	if err != nil {
		t.Fatalf("reopened.ListStepAttempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].StepID != "plan" {
		t.Fatalf("reopened attempts = %+v, want only plan attempt", attempts)
	}

	for _, tc := range []struct {
		name    string
		outcome AttemptOutcome
	}{
		{"canceled-route", AttemptOutcome{Status: AttemptStatusCanceled, ToStepID: "success"}},
		{"timed-out-index", AttemptOutcome{Status: AttemptStatusTimedOut, TransitionIndex: 1}},
		{"interrupted-match", AttemptOutcome{Status: AttemptStatusInterrupted, MatchDigest: "digest"}},
		{"canceled-decision", AttemptOutcome{Status: AttemptStatusCanceled, DecisionJSON: []byte(`{}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireErr(t, repo.CompleteStepAttempt(ctx, runID, first.AttemptID, 1, tc.outcome), ErrInvalidTransition, "terminal outcome with route data")
			attempt, err := repo.GetStepAttempt(ctx, runID, first.AttemptID)
			if err != nil {
				t.Fatalf("GetStepAttempt: %v", err)
			}
			if attempt.Status != AttemptStatusRunning || attempt.Version != 1 {
				t.Fatalf("attempt after rejected outcome = %+v, want running version 1", attempt)
			}
		})
	}
}

func TestIntegrationRecoverReportsClaimClearFailure(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemory()
	owner := NewStorageRepository(store)
	runID := "wfr-recover-clear-error"
	snap, snapshotJSON := newRun(t, runID)
	requireErr(t, owner.CreateRun(ctx, snap, snapshotJSON), nil, "CreateRun")
	requireErr(t, owner.CompareAndSetRunStatus(ctx, runID, 1, RunStatusRunning, nil), nil, "start run")
	requireErr(t, owner.CompareAndSetRunStatus(ctx, runID, 2, RunStatusSucceeded, nil), nil, "finish run")
	requireErr(t, owner.ClaimRun(ctx, runID, "owner"), nil, "ClaimRun")

	clearErr := errors.New("clear claim failed")
	recovering := NewStorageRepository(&clearFailStore{Store: store, err: clearErr})
	_, err := recovering.Recover(ctx)
	if !errors.Is(err, clearErr) {
		t.Fatalf("Recover error = %v, want %v", err, clearErr)
	}
	requireErr(t, store.ClaimRun(ctx, runID, "other"), storage.ErrClaimHeld, "claim remains after failed clear")
}

type clearFailStore struct {
	storage.Store
	err error
}

func (s *clearFailStore) ClearClaim(context.Context, string) error { return s.err }
