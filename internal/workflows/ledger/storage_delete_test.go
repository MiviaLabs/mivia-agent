package ledger

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// 20. DeleteRun
// ---------------------------------------------------------------------------

// TestStorageRepository_DeleteRunRemovesRunAndDerivedData deletes a settled run
// and asserts every read surface reports the run absent: GetRun, ListRuns,
// ListEvents, and every derived-data reader. A second delete is ErrNotFound.
func TestStorageRepository_DeleteRunRemovesRunAndDerivedData(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			fin := nowFixed()
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, 1, RunStatusRunning, nil), nil, "CAS running")
			requireErr(t, repo.CompareAndSetRunStatus(ctx, run, 2, RunStatusSucceeded, &fin), nil, "CAS succeeded")

			// Populate every derived-data surface.
			if _, err := repo.IncrementLoopCounter(ctx, run, "main"); err != nil {
				t.Fatalf("IncrementLoopCounter: %v", err)
			}
			if err := repo.CreateApproval(ctx, ApprovalRecord{
				ApprovalID: "ap-1", RunID: run, StepID: "gate", Status: "pending", CreatedAt: nowFixed(),
			}); err != nil {
				t.Fatalf("CreateApproval: %v", err)
			}
			if err := repo.UpsertDelivery(ctx, DeliveryRecord{
				RunID: run, IdempotencyKey: "k1", Mode: "draft", Status: "failed", UpdatedAt: nowFixed(),
			}); err != nil {
				t.Fatalf("UpsertDelivery: %v", err)
			}
			if err := repo.CreateStepAttempt(ctx, StepAttempt{
				AttemptID: "at-1", RunID: run, StepID: "impl", AttemptNo: 1,
				Status: AttemptStatusPending, StartedAt: nowFixed(),
			}); err != nil {
				t.Fatalf("CreateStepAttempt: %v", err)
			}

			requireErr(t, repo.DeleteRun(ctx, run), nil, "DeleteRun")

			if _, err := repo.GetRun(ctx, run); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetRun after delete = %v, want ErrNotFound", err)
			}
			for _, st := range []RunStatus{RunStatusSucceeded, RunStatusDeliveryPending} {
				runs, err := repo.ListRuns(ctx, st)
				if err != nil {
					t.Fatalf("ListRuns(%s): %v", st, err)
				}
				for _, r := range runs {
					if r.RunID == run {
						t.Fatalf("ListRuns(%s) still contains deleted run", st)
					}
				}
			}
			if _, err := repo.ListEvents(ctx, run, 0, 0); !errors.Is(err, ErrNotFound) {
				t.Fatalf("ListEvents after delete = %v, want ErrNotFound", err)
			}
			if _, err := repo.GetLoopCounters(ctx, run); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetLoopCounters after delete = %v, want ErrNotFound", err)
			}
			if _, err := repo.ListApprovals(ctx, run); !errors.Is(err, ErrNotFound) {
				t.Fatalf("ListApprovals after delete = %v, want ErrNotFound", err)
			}
			if _, err := repo.ListDeliveries(ctx, run); !errors.Is(err, ErrNotFound) {
				t.Fatalf("ListDeliveries after delete = %v, want ErrNotFound", err)
			}
			if _, err := repo.ListStepAttempts(ctx, run); !errors.Is(err, ErrNotFound) {
				t.Fatalf("ListStepAttempts after delete = %v, want ErrNotFound", err)
			}
			if _, err := repo.ListTransitions(ctx, run); !errors.Is(err, ErrNotFound) {
				t.Fatalf("ListTransitions after delete = %v, want ErrNotFound", err)
			}
			requireErr(t, repo.DeleteRun(ctx, run), ErrNotFound, "second DeleteRun")
		})
	}
}

// TestStorageRepository_DeleteRunMissingRun pins ErrNotFound for a run that was
// never created and for a run already deleted.
func TestStorageRepository_DeleteRunMissingRun(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			requireErr(t, repo.DeleteRun(ctx, "wfr-unknown-"+t.Name()), ErrNotFound, "DeleteRun unknown")
		})
	}
}

// TestStorageRepository_DeleteRunClearsClaim pins that deletion removes the
// run's claim row: a fresh claim succeeds immediately after delete.
func TestStorageRepository_DeleteRunClearsClaim(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			requireErr(t, repo.ClaimRun(ctx, run, "holder-1"), nil, "ClaimRun")
			requireErr(t, repo.DeleteRun(ctx, run), nil, "DeleteRun")
			requireErr(t, repo.ClaimRun(ctx, run, "holder-2"), nil, "ClaimRun after delete")
		})
	}
}

// TestStorageRepository_DeleteRunAllowsRecreateSameID pins that a run deleted
// and re-created under the same wfr- ID works, and that a re-created
// incarnation's deterministic attempt event ID does not collide with the
// deleted incarnation's (the deleted events are hard-removed; the tombstone
// carries its sequence in its ID).
func TestStorageRepository_DeleteRunAllowsRecreateSameID(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun #1")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{
				AttemptID: "at-1", RunID: run, StepID: "impl", AttemptNo: 1,
				Status: AttemptStatusPending, StartedAt: nowFixed(),
			}), nil, "CreateStepAttempt #1")
			requireErr(t, repo.DeleteRun(ctx, run), nil, "DeleteRun #1")

			snap2, json2 := newRun(t, run)
			snap2.WorkflowName = "second-incarnation"
			requireErr(t, repo.CreateRun(ctx, snap2, json2), nil, "CreateRun #2")
			requireErr(t, repo.CreateStepAttempt(ctx, StepAttempt{
				AttemptID: "at-1", RunID: run, StepID: "impl", AttemptNo: 1,
				Status: AttemptStatusPending, StartedAt: nowFixed(),
			}), nil, "CreateStepAttempt #2 (same deterministic ID)")

			got, err := repo.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun #2: %v", err)
			}
			if got.WorkflowName != "second-incarnation" {
				t.Fatalf("GetRun.WorkflowName = %q, want second-incarnation", got.WorkflowName)
			}
			requireErr(t, repo.DeleteRun(ctx, run), nil, "DeleteRun #2")
			requireErr(t, repo.DeleteRun(ctx, run), ErrNotFound, "DeleteRun #3")
		})
	}
}

// TestStorageRepository_DeleteRunConvergesInSecondReader is the workflows-ledger
// analogue of the coordinator's tombstone convergence pin: a second repository
// instance over the same store must stop serving a run after another instance
// deletes it (a hard delete without a tombstone would leave the second
// instance's watermark stuck and its cached projection serving the deleted
// snapshot forever).
func TestStorageRepository_DeleteRunConvergesInSecondReader(t *testing.T) {
	ctx := context.Background()
	for _, pair := range repoPairs() {
		t.Run(pair.name, func(t *testing.T) {
			a, b, done := pair.new(t)
			defer done()
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, a.CreateRun(ctx, snap, json), nil, "CreateRun (A)")
			fin := nowFixed()
			requireErr(t, a.CompareAndSetRunStatus(ctx, run, 1, RunStatusRunning, nil), nil, "CAS running (A)")
			requireErr(t, a.CompareAndSetRunStatus(ctx, run, 2, RunStatusDeliveryPending, &fin), nil, "CAS delivery_pending (A)")

			// B catches the run up so its watermark sits past the run's events.
			if _, err := b.GetRun(ctx, run); err != nil {
				t.Fatalf("GetRun (B) before delete: %v", err)
			}

			requireErr(t, a.DeleteRun(ctx, run), nil, "DeleteRun (A)")

			if _, err := b.GetRun(ctx, run); !errors.Is(err, ErrNotFound) {
				t.Fatalf("GetRun (B) after delete = %v, want ErrNotFound", err)
			}
			runs, err := b.ListRuns(ctx)
			if err != nil {
				t.Fatalf("ListRuns (B): %v", err)
			}
			for _, r := range runs {
				if r.RunID == run {
					t.Fatalf("ListRuns (B) still contains deleted run")
				}
			}
			requireErr(t, b.DeleteRun(ctx, run), ErrNotFound, "DeleteRun (B) after A deleted")
		})
	}
}

// TestStorageRepository_DeleteRunRecreateConvergesInSecondReader pins that a
// second instance that had caught up the DELETED incarnation sees the NEW
// incarnation of the same wfr- ID after recreation (never the stale deleted
// snapshot).
func TestStorageRepository_DeleteRunRecreateConvergesInSecondReader(t *testing.T) {
	ctx := context.Background()
	for _, pair := range repoPairs() {
		t.Run(pair.name, func(t *testing.T) {
			a, b, done := pair.new(t)
			defer done()
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, a.CreateRun(ctx, snap, json), nil, "CreateRun #1 (A)")
			if _, err := b.GetRun(ctx, run); err != nil {
				t.Fatalf("GetRun (B) incarnation 1: %v", err)
			}
			requireErr(t, a.DeleteRun(ctx, run), nil, "DeleteRun (A)")

			snap2, json2 := newRun(t, run)
			snap2.WorkflowName = "second-incarnation"
			requireErr(t, a.CreateRun(ctx, snap2, json2), nil, "CreateRun #2 (A)")

			got, err := b.GetRun(ctx, run)
			if err != nil {
				t.Fatalf("GetRun (B) incarnation 2: %v", err)
			}
			if got.WorkflowName != "second-incarnation" {
				t.Fatalf("GetRun (B).WorkflowName = %q, want second-incarnation", got.WorkflowName)
			}
		})
	}
}

// TestStorageRepository_DeleteRunLeavesContentUntouched pins that deleting a
// run never removes shared content-addressed blobs (the store contract: a
// delete never deletes content).
func TestStorageRepository_DeleteRunLeavesContentUntouched(t *testing.T) {
	ctx := context.Background()
	for name, repo := range repos(t) {
		t.Run(name, func(t *testing.T) {
			run := runID(t)
			snap, json := newRun(t, run)
			requireErr(t, repo.CreateRun(ctx, snap, json), nil, "CreateRun")
			ref := "ref:output:deadbeef"
			data := []byte("shared content")
			requireErr(t, repo.StoreContent(ctx, ref, data), nil, "StoreContent")
			requireErr(t, repo.DeleteRun(ctx, run), nil, "DeleteRun")
			got, err := repo.LoadContent(ctx, ref)
			if err != nil {
				t.Fatalf("LoadContent after delete: %v", err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("LoadContent = %q, want %q", got, data)
			}
		})
	}
}
