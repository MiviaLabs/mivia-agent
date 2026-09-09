package controller

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// bareStepCoordinator implements stepCoordinator only - deliberately no
// LoadTaskToolCalls - so it fails the toolCallTraceSource assertion
// fetchToolExecutionHistory makes.
type bareStepCoordinator struct{}

func (bareStepCoordinator) EnsureRun(context.Context, coordinator.EnsureRunRequest) (*coordinator.RunHandle, error) {
	return nil, nil
}
func (bareStepCoordinator) EnsureSingleTaskRun(context.Context, coordinator.EnsureRunRequest) (*coordinator.RunHandle, error) {
	return nil, nil
}
func (bareStepCoordinator) EnsureTerminalSingleTaskRun(context.Context, coordinator.EnsureRunRequest, ledger.TaskStatus) (*coordinator.RunHandle, error) {
	return nil, nil
}
func (bareStepCoordinator) JoinAsRecovered(context.Context, coordinator.EnsureRunRequest) (*coordinator.RunHandle, error) {
	return nil, nil
}
func (bareStepCoordinator) Inspect(context.Context, *coordinator.RunHandle) (ledger.RunSnapshot, error) {
	return ledger.RunSnapshot{}, nil
}
func (bareStepCoordinator) Join(context.Context, *coordinator.RunHandle) (*coordinator.RunResult, error) {
	return nil, nil
}
func (bareStepCoordinator) Cancel(context.Context, *coordinator.RunHandle) error { return nil }

// TestFetchToolExecutionHistory_CoordinatorWithoutTraceSourceReturnsNil
// pins fetchToolExecutionHistory's own toolCallTraceSource type-assertion
// guard: a coordinator that satisfies stepCoordinator but not the
// LoadTaskToolCalls-carrying optional interface must yield no history
// (fail-closed), not a panic.
func TestFetchToolExecutionHistory_CoordinatorWithoutTraceSourceReturnsNil(t *testing.T) {
	r := &CoordinatorRunner{Coordinator: bareStepCoordinator{}}
	got, err := r.fetchToolExecutionHistory(context.Background(), "run-1", "task-1")
	if err != nil {
		t.Fatalf("fetchToolExecutionHistory() error = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("fetchToolExecutionHistory() = %v, want nil for a coordinator with no trace source", got)
	}
}
