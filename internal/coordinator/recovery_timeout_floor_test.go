package coordinator

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// floorCoordinatorAndSnapshot builds a coordinator whose pool has no timeout
// ceiling configured and a single non-terminal task snapshot carrying the
// given persisted Timeout.
func floorCoordinatorAndSnapshot(t *testing.T, poolTimeout, snapTimeout time.Duration) (Coordinator, ledger.TaskSnapshot) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	_ = d.Register(runtime.Subagent, "worker", staticHandler{out: json.RawMessage(`{"ok":true}`)})
	p := subagents.New(d, subagents.Policy{Workers: 1, Timeout: poolTimeout})
	c := New(repo, p)
	snap := ledger.TaskSnapshot{
		TaskID:      "t1",
		Status:      string(ledger.TaskStatusRunning),
		HandlerName: "worker",
		AgentName:   "worker",
		AgentDigest: "test-digest",
		Input:       json.RawMessage(`{"prompt":"work"}`),
		Timeout:     snapTimeout,
	}
	return c, snap
}

// TestTasksFromSnapshotsAppliesTimeoutFloor is the resume half of the
// EffectiveTimeoutSec guarantee. A task persisted with Timeout == 0 means
// "the run's parent budget applies" (context.WithCancel in executeOne), but a
// resumed run has no live parent - so the restored task must carry the 12h
// safety floor instead, or the resumed execution would be unbounded.
func TestTasksFromSnapshotsAppliesTimeoutFloor(t *testing.T) {
	c, snap := floorCoordinatorAndSnapshot(t, 0, 0)
	tasks, _, err := c.(*coordinator).tasksFromSnapshots(context.Background(), []ledger.TaskSnapshot{snap})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Duration(config.DefaultOrchestrationTimeoutSec) * time.Second
	if got := tasks[0].Timeout; got != want {
		t.Fatalf("persisted timeout 0 must restore to the %v safety floor, got %v", want, got)
	}
}

// TestTasksFromSnapshotsKeepsPositiveTimeout verifies a positive persisted
// timeout below the ceiling is preserved: the floor must not override work
// that already carries a finite budget.
func TestTasksFromSnapshotsKeepsPositiveTimeout(t *testing.T) {
	c, snap := floorCoordinatorAndSnapshot(t, 0, 30*time.Second)
	tasks, _, err := c.(*coordinator).tasksFromSnapshots(context.Background(), []ledger.TaskSnapshot{snap})
	if err != nil {
		t.Fatal(err)
	}
	if got := tasks[0].Timeout; got != 30*time.Second {
		t.Fatalf("positive persisted timeout must be preserved, got %v", got)
	}
}

// TestTasksFromSnapshotsClampsTimeoutToPoolCeiling verifies the upper clamp
// still wins over the floor: a ledger claiming a larger timeout than the live
// pool policy allows must not raise the ceiling.
func TestTasksFromSnapshotsClampsTimeoutToPoolCeiling(t *testing.T) {
	c, snap := floorCoordinatorAndSnapshot(t, time.Minute, 2*time.Minute)
	tasks, _, err := c.(*coordinator).tasksFromSnapshots(context.Background(), []ledger.TaskSnapshot{snap})
	if err != nil {
		t.Fatal(err)
	}
	if got := tasks[0].Timeout; got != time.Minute {
		t.Fatalf("persisted timeout must be clamped to pool ceiling %v, got %v", time.Minute, got)
	}
}
