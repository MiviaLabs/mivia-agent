package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// admitRunWithReferral admits a single-task keyed run, spawns a same-run
// referral task (SpawnReferral), and joins until the run reaches a terminal
// state. The shared repository then holds two terminal tasks for the run: the
// originally admitted task and the referral. It returns the admitted request
// (run/key/task) and the referral task ID so callers can rejoin.
func admitRunWithReferral(t *testing.T, repo ledger.LedgerRepository, admit func(Coordinator, EnsureRunRequest) (*RunHandle, error)) (EnsureRunRequest, string) {
	t.Helper()
	creator := newIdempotencyCoordinator(repo)
	req := EnsureRunRequest{RunID: NewRunID(), Tasks: []subagents.Task{idempotencyTask()}, IdempotencyKey: "rejoin-after-referral"}
	h, err := admit(creator, req)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	refID, err := creator.SpawnReferral(context.Background(), h.RunID(), subagents.Task{
		ID:        "task-referral",
		Name:      "worker",
		AgentName: "worker",
		Input:     json.RawMessage(`"referral work"`),
	}, "")
	if err != nil {
		t.Fatalf("spawn referral: %v", err)
	}
	if _, err := creator.Join(context.Background(), h); err != nil {
		t.Fatalf("join: %v", err)
	}
	stored, err := repo.ListTasks(context.Background(), req.RunID)
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored tasks = %d, err = %v; want the admitted task plus the referral", len(stored), err)
	}
	return req, refID
}

// J-1: an idempotent rejoin with the same key, run, and requested work must
// resolve to the admitted run even after a same-run referral grew the stored
// task set beyond the request. The exact one-task count guards treated the
// referral as "partial admission" or a count mismatch and bricked the key.
func TestEnsureSingleTaskRunRejoinAfterReferral(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	req, _ := admitRunWithReferral(t, repo, func(c Coordinator, req EnsureRunRequest) (*RunHandle, error) {
		return c.EnsureSingleTaskRun(context.Background(), req)
	})
	recovered := newIdempotencyCoordinator(repo)
	h, err := recovered.EnsureSingleTaskRun(context.Background(), req)
	if err != nil {
		t.Fatalf("rejoin after referral: %v", err)
	}
	if h == nil || h.RunID() != req.RunID {
		t.Fatalf("rejoin handle = %+v, want run %q", h, req.RunID)
	}
}

func TestJoinAsRecoveredRejoinAfterReferral(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	req, _ := admitRunWithReferral(t, repo, func(c Coordinator, req EnsureRunRequest) (*RunHandle, error) {
		return c.EnsureSingleTaskRun(context.Background(), req)
	})
	recovered := newIdempotencyCoordinator(repo)
	h, err := recovered.JoinAsRecovered(context.Background(), req)
	if err != nil {
		t.Fatalf("rejoin after referral: %v", err)
	}
	if h == nil || h.RunID() != req.RunID {
		t.Fatalf("rejoin handle = %+v, want run %q", h, req.RunID)
	}
}

func TestEnsureRunRejoinAfterReferral(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	req, _ := admitRunWithReferral(t, repo, func(c Coordinator, req EnsureRunRequest) (*RunHandle, error) {
		return c.EnsureRun(context.Background(), req)
	})
	recovered := newIdempotencyCoordinator(repo)
	h, err := recovered.EnsureRun(context.Background(), req)
	if err != nil {
		t.Fatalf("rejoin after referral: %v", err)
	}
	if h == nil || h.RunID() != req.RunID {
		t.Fatalf("rejoin handle = %+v, want run %q", h, req.RunID)
	}
}

// Negative paths: work that was never admitted must still conflict, even when
// the run now stores the referral task. The request-level fingerprint still
// proves the rejoin is exactly the admitted work, so a referral-only task or
// an admitted task with altered input cannot slip through the relaxed guards.
func TestRejoinAfterReferralRejectsNonAdmittedWork(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	req, refID := admitRunWithReferral(t, repo, func(c Coordinator, req EnsureRunRequest) (*RunHandle, error) {
		return c.EnsureSingleTaskRun(context.Background(), req)
	})
	recovered := newIdempotencyCoordinator(repo)

	referralOnly := EnsureRunRequest{
		RunID:          req.RunID,
		IdempotencyKey: req.IdempotencyKey,
		Tasks:          []subagents.Task{{ID: refID, Name: "worker", AgentName: "worker", Input: json.RawMessage(`"referral work"`)}},
	}
	if _, err := recovered.EnsureSingleTaskRun(context.Background(), referralOnly); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("referral-only rejoin error = %v, want %v", err, ErrIdempotencyConflict)
	}
	if _, err := recovered.JoinAsRecovered(context.Background(), referralOnly); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("referral-only JoinAsRecovered error = %v, want %v", err, ErrIdempotencyConflict)
	}

	changed := idempotencyTask()
	changed.Input = json.RawMessage(`"changed work"`)
	changedReq := EnsureRunRequest{RunID: req.RunID, IdempotencyKey: req.IdempotencyKey, Tasks: []subagents.Task{changed}}
	if _, err := recovered.EnsureSingleTaskRun(context.Background(), changedReq); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed-input rejoin error = %v, want %v", err, ErrIdempotencyConflict)
	}
}

// stored < requested must stay fail-closed: a run that durably admits fewer
// tasks than the request is genuine partial admission, not a referral superset.
func TestEnsureRunStillRejectsStoredLessThanRequested(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	first := idempotencyTask()
	second := first
	second.ID = "task-2"
	runID := NewRunID()
	seedEnsuredRun(t, repo, context.Background(), runID, "partial-after-referral", []subagents.Task{first, second}, 1)
	c := newIdempotencyCoordinator(repo)
	before, err := repo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.EnsureRun(context.Background(), EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{first, second}, IdempotencyKey: "partial-after-referral"})
	if err == nil {
		t.Fatal("EnsureRun accepted stored<requested partial admission")
	}
	// Rejection must not mutate the run. The in-memory repository derives the
	// run status from its stored tasks (a queued task reads back as "queued"),
	// so compare against the pre-call snapshot and confirm the missing task was
	// not created to repair the partial admission.
	after, getErr := repo.GetRun(context.Background(), runID)
	if getErr != nil {
		t.Fatalf("run lookup after rejection: %v", getErr)
	}
	if after.Status != before.Status {
		t.Fatalf("run changed after rejection: status = %q, want %q (unchanged)", after.Status, before.Status)
	}
	stored, listErr := repo.ListTasks(context.Background(), runID)
	if listErr != nil || len(stored) != 1 {
		t.Fatalf("stored tasks after rejection = %d, err = %v; want the single admitted task", len(stored), listErr)
	}
}
