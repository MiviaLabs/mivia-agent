package coordinator

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestCancelOnlyPathWorksWithNilPool proves the cancel-only admission and
// cancel path (EnsureTerminalSingleTaskRun -> Join) never dereferences a nil
// subagent pool. This backs the Wave 7 CLI cancel-coordinator design
// decision: a coordinator built for cancel-only use (cliPanelCancelCoordinator
// in internal/cli) carries a nil pool rather than a full provider-backed one,
// because an operator must be able to cancel a run even with broken or
// missing provider credentials (D15).
func TestCancelOnlyPathWorksWithNilPool(t *testing.T) {
	coord := New(ledger.NewMemoryLedgerRepository(), nil)
	task := subagents.Task{ID: "task-tombstone", Name: "worker", Input: []byte(`"work"`)}
	h, err := coord.EnsureTerminalSingleTaskRun(context.Background(), EnsureRunRequest{RunID: NewRunID(), Tasks: []subagents.Task{task}, IdempotencyKey: "tombstone"}, ledger.TaskStatusCanceled)
	if err != nil {
		t.Fatalf("EnsureTerminalSingleTaskRun with a nil pool: %v", err)
	}
	result, err := coord.Join(context.Background(), h)
	if err != nil || result.Snapshot.Status != ledger.RunStatusCanceled {
		t.Fatalf("join = %+v, %v", result, err)
	}
}

// TestJoinAsRecoveredWorksWithNilPool proves JoinAsRecovered - the other
// half of the panel cancel path (a live/queued child, not yet terminal) -
// also never dereferences a nil pool. It never dispatches, so ErrNotFound
// on an unadmitted run is the expected, pool-independent outcome.
func TestJoinAsRecoveredWorksWithNilPool(t *testing.T) {
	coord := New(ledger.NewMemoryLedgerRepository(), nil)
	task := subagents.Task{ID: "task-live", Name: "worker", Input: []byte(`"work"`)}
	_, err := coord.JoinAsRecovered(context.Background(), EnsureRunRequest{RunID: NewRunID(), Tasks: []subagents.Task{task}, IdempotencyKey: "live"})
	if err != ledger.ErrNotFound {
		t.Fatalf("JoinAsRecovered with a nil pool, no admitted run: err = %v, want ledger.ErrNotFound", err)
	}
}
