package coordinator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
)

// TestTasksFromSnapshotsRestoresRawID pins a resume-path regression: a
// dispatch_tasks task's real internal id is namespace+":"+RawID
// (internal/cliorchestrate/dispatch.go's buildTasks), and RawID is
// persisted onto ledger.TaskSnapshot alongside it so join_run/
// inspect_agents/run_messages can report the model's own raw id back
// without any string-heuristic (see internal/cliorchestrate/
// task_namespace.go's modelVisibleTaskID). taskFromSnapshot - the
// snapshot -> subagents.Task path a resumed run's coordinator rebuilds
// live tasks through - must carry RawID across that round-trip, or a
// resumed session's task loses its raw id and join_run/inspect_agents
// leak the internal namespaced form for every task recovered this way.
func TestTasksFromSnapshotsRestoresRawID(t *testing.T) {
	c, _ := buildTestCoordinator(t)
	ctx := context.Background()

	snaps := []ledger.TaskSnapshot{
		{
			TaskID:      "ns-resume:solo",
			RawID:       "solo",
			Status:      string(ledger.TaskStatusRunning),
			HandlerName: "worker",
			AgentName:   "worker",
			AgentDigest: "test-digest",
			Input:       json.RawMessage(`{"prompt":"still-going"}`),
		},
	}

	tasks, _, err := c.tasksFromSnapshots(ctx, snaps)
	if err != nil {
		t.Fatalf("tasksFromSnapshots returned error: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 non-terminal task, got %d", len(tasks))
	}
	if tasks[0].ID != "ns-resume:solo" {
		t.Errorf("expected the real namespaced id to survive, got %q", tasks[0].ID)
	}
	if tasks[0].RawID != "solo" {
		t.Errorf("expected RawID %q to survive taskFromSnapshot, got %q", "solo", tasks[0].RawID)
	}
}
