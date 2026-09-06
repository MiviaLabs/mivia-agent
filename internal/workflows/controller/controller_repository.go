package controller

import (
	"context"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// controllerRepository is this package's consumer-side view of the workflow
// ledger: the run, attempt, loop-counter, approval, panel-phase, content, and
// claim members step execution, advancement, and cancellation use. The full
// ledger contract carries delivery, event, recovery, and admin members this
// package never touches; it depends on the subset, not the fat interface.
// workflowledger.Repository satisfies it.
type controllerRepository interface {
	CreateRun(ctx context.Context, snap workflowledger.RunSnapshot, snapshotJSON []byte) error
	GetRun(ctx context.Context, runID string) (workflowledger.RunSnapshot, error)
	GetRunSnapshot(ctx context.Context, runID string) ([]byte, error)
	CompareAndSetRunStatus(ctx context.Context, runID string, expectedVersion uint64, status workflowledger.RunStatus, finishedAt *time.Time) error
	CreateStepAttempt(ctx context.Context, attempt workflowledger.StepAttempt) error
	GetStepAttempt(ctx context.Context, runID, attemptID string) (workflowledger.StepAttempt, error)
	ListStepAttempts(ctx context.Context, runID string) ([]workflowledger.StepAttempt, error)
	CompleteStepAttempt(ctx context.Context, runID, attemptID string, expectedVersion uint64, outcome workflowledger.AttemptOutcome) error
	SetStepAttemptPrompt(ctx context.Context, runID, attemptID, promptRef string) error
	SetStepAttemptExecution(ctx context.Context, runID, attemptID, coordinatorRunID, taskID, reason string) error
	SetStepAttemptHeartbeat(ctx context.Context, runID, attemptID string, heartbeatAt time.Time) error
	ListTransitions(ctx context.Context, runID string) ([]workflowledger.TransitionRecord, error)
	IncrementLoopCounter(ctx context.Context, runID, loopName string) (int, error)
	GetLoopCounters(ctx context.Context, runID string) ([]workflowledger.LoopCounter, error)
	CreateApproval(ctx context.Context, a workflowledger.ApprovalRecord) error
	ResolveApproval(ctx context.Context, runID, approvalID, actor, status, reason string) error
	ListApprovals(ctx context.Context, runID string) ([]workflowledger.ApprovalRecord, error)
	ClaimRun(ctx context.Context, runID, holder string) error
	RefreshRunClaim(ctx context.Context, runID, holder string) error
	ReleaseRun(ctx context.Context, runID, holder string) error
	CompareAndSetPanelPhase(ctx context.Context, runID string, attemptID string, expectedVersion uint64, from workflowledger.PanelPhase, to workflowledger.PanelPhase, synthesis *workflowledger.PanelSynthesisExecution) error
	RecordRunResumed(ctx context.Context, runID string) error
	StoreContent(ctx context.Context, ref string, data []byte) error
	LoadContent(ctx context.Context, ref string) ([]byte, error)
}

// Compile-time check that the shipped ledger repository satisfies this subset.
var _ controllerRepository = (*workflowledger.StorageRepository)(nil)
