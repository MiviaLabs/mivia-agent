package cliorchestrate

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// ResumeCoordinator is the consumer-side subset the /resume paths need: list
// interrupted runs and resume one of them. The full coordinator carries far
// more; the resume surface depends on this subset, not the fat interface.
// The real coordinator type satisfies it; so can a test double that
// implements only these two methods.
type ResumeCoordinator interface {
	ListInterruptedRuns(ctx context.Context) ([]coordinator.RecoveredRun, error)
	ResumeInterruptedRun(ctx context.Context, runID string) (*coordinator.RunHandle, error)
}

// OrchestrationCoordinator is the consumer-side subset this package's
// dispatch, inspect, join, cancel, salvage, and park-query paths use. It is
// the full set of coordinator members cliorchestrate touches; callers outside
// this package that only resume runs depend on the narrower
// ResumeCoordinator instead.
type OrchestrationCoordinator interface {
	ResumeCoordinator
	Spawn(ctx context.Context, tasks []subagents.Task, idempotencyKey string) (*coordinator.RunHandle, error)
	SpawnNew(ctx context.Context, tasks []subagents.Task, idempotencyKey string) (*coordinator.RunHandle, bool, error)
	Inspect(ctx context.Context, h *coordinator.RunHandle) (ledger.RunSnapshot, error)
	Join(ctx context.Context, h *coordinator.RunHandle) (*coordinator.RunResult, error)
	Cancel(ctx context.Context, h *coordinator.RunHandle) error
	ParkedQuestions(runID string) []coordinator.ParkedQuestion
	RegisterSubagentToolCanceler(runID, taskID string, canceler agent.ToolCanceler)
}
