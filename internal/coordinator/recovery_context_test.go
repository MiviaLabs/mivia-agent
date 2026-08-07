package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// slowLoadContentRepo wraps a MemoryLedgerRepository with an artificially slow
// LoadContent that blocks until the provided context is done. This allows
// tests to detect a context.Background() leak: if the method under test uses
// context.Background() instead of the caller's context, the load will never
// observe cancellation and the test times out.
type slowLoadContentRepo struct {
	*ledger.MemoryLedgerRepository
	loadDelay time.Duration
}

func (r *slowLoadContentRepo) LoadContent(ctx context.Context, ref string) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(r.loadDelay):
		return []byte(`{"output":"loaded"}`), nil
	}
}

// TestTerminalTaskResultLeaksContext verifies that the coordinator method
// terminalTaskResult does NOT leak context.Background() into LoadContent.
//
// Before the fix, (c *coordinator) terminalTaskResult in recovery_output.go
// shadowed the package-level terminalTaskResult in recovery.go and called
// LoadContent(context.Background(), snap.OutputRef), which bypassed the
// caller's context deadline/cancellation.
//
// This test:
//  1. Creates a coordinator with a slow-load-content repo.
//  2. Stores a task with an output reference (completed, non-empty OutputRef).
//  3. Calls c.terminalTaskResultWithOutput with an already-canceled context.
//  4. Asserts that LoadContent respects the canceled context (returns error),
//     not blocks indefinitely or succeeds via context.Background().
//
// A non-terminal task (e.g., queued) should return false without any
// LoadContent call.
func TestTerminalTaskResultLeaksContext(t *testing.T) {
	slowRepo := &slowLoadContentRepo{
		MemoryLedgerRepository: ledger.NewMemoryLedgerRepository(),
		loadDelay:              5 * time.Second,
	}

	// Create a minimal run and task with a completed status and output ref.
	runID := "run-ctx-leak-test"
	taskID := "task-ctx-leak"
	ctx := context.Background()

	_ = slowRepo.CreateRun(ctx, "", ledger.RunSnapshot{
		RunID:  runID,
		Status: ledger.RunStatusRunning,
	})
	// Store output content so the reference resolves.
	outputRef := "ref:output:test123"
	_ = slowRepo.StoreContent(ctx, outputRef, []byte(`{"result":"data"}`))
	_ = slowRepo.CreateTask(ctx, ledger.TaskSnapshot{
		RunID:     runID,
		TaskID:    taskID,
		Status:    string(ledger.TaskStatusCompleted),
		OutputRef: outputRef,
		Version:   1,
	})

	// Build a coordinator (pool can be nil; terminalTaskResult doesn't use it).
	c := &coordinator{repo: slowRepo}

	snap := ledger.TaskSnapshot{
		RunID:     runID,
		TaskID:    taskID,
		Status:    string(ledger.TaskStatusCompleted),
		OutputRef: outputRef,
	}

	// Call with an already-canceled context.
	// If the method leaks context.Background(), LoadContent will block for
	// loadDelay (5s) and succeed. If the method properly propagates the
	// canceled context, LoadContent returns ctx.Err() immediately.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// The package-level function terminalTaskResult does NOT call LoadContent
	// at all — it returns (subagents.Result{}, false) for completed tasks
	// with no body content. The method on coordinator wraps it and adds
	// LoadContent, which is the bug.
	//
	// After the fix (renamed to terminalTaskResultWithOutput accepting ctx),
	// this should return quickly with the context error, not block.
	result, terminal := c.terminalTaskResultWithOutput(canceledCtx, snap)
	if !terminal {
		t.Fatal("expected terminal=true for completed task")
	}

	// With the fix, the Output field should be empty because LoadContent
	// was called with a canceled context and returned an error.
	if result.Output != nil {
		t.Errorf("Output = %s, want nil (LoadContent should fail with canceled context)", result.Output)
	}
}
