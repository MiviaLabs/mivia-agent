package coordinator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

type pauseAfterCreateRepository struct {
	ledger.LedgerRepository
	created chan struct{}
	resume  chan struct{}
}

type ownershipFenceRepository struct {
	ledger.LedgerRepository
	held            bool
	deleteWhileHeld bool
}

func (r *ownershipFenceRepository) ClaimRun(ctx context.Context, runID, holder string) error {
	if err := r.LedgerRepository.ClaimRun(ctx, runID, holder); err != nil {
		return err
	}
	r.held = true
	return nil
}

func (r *ownershipFenceRepository) AppendEvent(context.Context, ledger.LifecycleEvent) error {
	return errors.New("forced append failure")
}

func (r *ownershipFenceRepository) DeleteRun(ctx context.Context, runID string) error {
	r.deleteWhileHeld = r.held
	return r.LedgerRepository.DeleteRun(ctx, runID)
}

func (r *ownershipFenceRepository) ReleaseRun(ctx context.Context, runID, holder string) error {
	r.held = false
	return r.LedgerRepository.ReleaseRun(ctx, runID, holder)
}

func (r *pauseAfterCreateRepository) CreateRun(ctx context.Context, key string, snap ledger.RunSnapshot) error {
	if err := r.LedgerRepository.CreateRun(ctx, key, snap); err != nil {
		return err
	}
	close(r.created)
	<-r.resume
	return nil
}

func TestEnsureRunClaimLoserDoesNotDeleteWinnerAdmission(t *testing.T) {
	store := storage.NewMemory()
	loserBase := ledger.NewStorageLedgerRepository(store)
	winner := ledger.NewStorageLedgerRepository(store)
	paused := &pauseAfterCreateRepository{
		LedgerRepository: loserBase,
		created:          make(chan struct{}),
		resume:           make(chan struct{}),
	}
	loser := newIdempotencyCoordinator(paused)
	task := idempotencyTask()
	runID := NewRunID()
	req := EnsureRunRequest{RunID: runID, Tasks: []subagents.Task{task}, IdempotencyKey: "step"}
	errCh := make(chan error, 1)
	go func() {
		_, err := loser.EnsureRun(context.Background(), req)
		errCh <- err
	}()

	select {
	case <-paused.created:
	case <-time.After(2 * time.Second):
		t.Fatal("losing creator did not pause after CreateRun")
	}
	if err := winner.ClaimRun(context.Background(), runID, "winner"); err != nil {
		t.Fatal(err)
	}
	seedTask := ledger.TaskSnapshot{
		RunID: runID, TaskID: task.ID, DisplayName: task.Name, HandlerName: task.Name,
		AgentName: task.AgentName, AgentDigest: task.AgentDigest, Skill: task.Skill,
		ProviderName: task.ProviderName, Model: task.Model, Scope: task.Scope,
		OutputSchema: task.OutputSchema, Input: task.Input, Timeout: task.Timeout,
		Budget: task.Budget, Status: string(ledger.TaskStatusQueued), Version: 1,
		Attempts: []ledger.AttemptSnapshot{{
			AttemptID: "winner-attempt", TaskID: task.ID, RunID: runID,
			AttemptNum: 1, Status: string(ledger.TaskStatusQueued),
		}},
	}
	if err := winner.CreateTask(context.Background(), seedTask); err != nil {
		t.Fatal(err)
	}
	close(paused.resume)
	if err := <-errCh; !errors.Is(err, ErrRunHeldByAnotherExecutor) {
		t.Fatalf("loser error = %v, want ErrRunHeldByAnotherExecutor", err)
	}

	if _, err := winner.GetRun(context.Background(), runID); err != nil {
		t.Fatalf("winner run was deleted: %v", err)
	}
	if tasks, err := winner.ListTasks(context.Background(), runID); err != nil || len(tasks) != 1 {
		t.Fatalf("winner tasks = %d, err = %v", len(tasks), err)
	}
	if err := winner.ReleaseRun(context.Background(), runID, "winner"); err != nil {
		t.Fatalf("winner claim was cleared: %v", err)
	}
}

func TestRunCreationCleanupDeletesOnlyWhileClaimIsHeld(t *testing.T) {
	repo := &ownershipFenceRepository{LedgerRepository: ledger.NewMemoryLedgerRepository()}
	c := newIdempotencyCoordinator(repo)
	_, err := c.EnsureRun(context.Background(), EnsureRunRequest{
		RunID: NewRunID(), Tasks: []subagents.Task{idempotencyTask()}, IdempotencyKey: "step",
	})
	if err == nil {
		t.Fatal("EnsureRun succeeded after the forced event failure")
	}
	if !repo.deleteWhileHeld {
		t.Fatal("cleanup deleted the run without an execution claim")
	}
}
