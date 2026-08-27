package cliworkflow

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// TestCliPanelCancelCoordinatorReusesLiveInstance mirrors
// localengine.TestPanelCancelCoordinatorReusesLiveInstance: when a run is
// live in this process, cliPanelCancelCoordinator must return that exact
// coordinator instance (the one panel children were actually dispatched
// through), not a fresh one that can never find or genuinely cancel them.
func TestCliPanelCancelCoordinatorReusesLiveInstance(t *testing.T) {
	live := coordinator.New(nil, nil)
	liveRunner := controller.NewCoordinatorRunner(live)

	if got := cliPanelCancelCoordinator(liveRunner, nil); got != live {
		t.Fatalf("cliPanelCancelCoordinator(liveRunner, nil) = %p, want the live run's own dispatching coordinator %p", got, live)
	}
}

// TestCliPanelCancelCoordinatorBuildsMinimalFromStore proves the fallback
// path (no in-process runner, e.g. the one-shot `mivia workflow cancel` CLI
// or a cross-process session cancel) builds a coordinator over the given
// store and that the coordinator is genuinely usable for cancel-only
// admission with its nil subagent pool - not merely non-nil.
func TestCliPanelCancelCoordinatorBuildsMinimalFromStore(t *testing.T) {
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "cancel-coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	coord := cliPanelCancelCoordinator(nil, store)
	if coord == nil {
		t.Fatal("cliPanelCancelCoordinator(nil, store) = nil, want a usable cancel-only coordinator")
	}
	task := subagents.Task{ID: "task-tombstone", Name: "worker", Input: []byte(`"work"`)}
	h, err := coord.EnsureTerminalSingleTaskRun(context.Background(), coordinator.EnsureRunRequest{
		RunID:          coordinator.NewRunID(),
		IdempotencyKey: "cancel-coord-tombstone",
		Tasks:          []subagents.Task{task},
	}, ledger.TaskStatusCanceled)
	if err != nil {
		t.Fatalf("EnsureTerminalSingleTaskRun on the store-backed coordinator: %v", err)
	}
	result, err := coord.Join(context.Background(), h)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if string(result.Snapshot.Status) != "canceled" {
		t.Fatalf("run status = %q, want canceled", result.Snapshot.Status)
	}
}

// TestCliPanelCancelCoordinatorNilWithoutStore documents the defensive nil
// fallback: with neither a live runner nor a store, there is nothing to
// build a coordinator from. CancelRunWithAttemptsWithClaim fails closed on a
// nil coordinator rather than panicking.
func TestCliPanelCancelCoordinatorNilWithoutStore(t *testing.T) {
	if got := cliPanelCancelCoordinator(nil, nil); got != nil {
		t.Fatalf("cliPanelCancelCoordinator(nil, nil) = %v, want nil", got)
	}
}
