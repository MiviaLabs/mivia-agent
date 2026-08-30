package coordinator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestTasksFromSnapshotsPlainHandlerWithLiveAuthority is the deterministic
// pin for the macOS CI race in
// TestSQLiteSingleTaskTerminalAndRunnableAdmissionRace: two executors race
// one single-task admission, the loser adopts the winner's snapshot through
// the resume path, and taskFromSnapshot rejected the snapshot because the
// task routes by a plain registered handler (no agent routing snapshot).
// Adoption must not be stricter than fresh admission: when the adopting
// caller re-supplies the identical live task (same id, same handler, no
// agent routing of its own), the snapshot is dispatchable on exactly the
// authority ValidateTask re-checks. Without a live counterpart the old
// undispatchable rejection must stay - that is the older-version/
// unresolvable-handler safety net.
func TestTasksFromSnapshotsPlainHandlerWithLiveAuthority(t *testing.T) {
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "worker", invoker(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	c := New(ledger.NewMemoryLedgerRepository(), subagents.New(dispatcher, subagents.Policy{Workers: 1})).(*coordinator)

	snaps := []ledger.TaskSnapshot{{
		RunID: "r1", TaskID: "task-panel", HandlerName: "worker",
		Status: string(ledger.TaskStatusQueued), Input: json.RawMessage(`"work"`),
	}}
	live := []subagents.Task{{ID: "task-panel", Name: "worker", Input: json.RawMessage(`"work"`)}}

	tasks, _, err := c.tasksFromSnapshotsWithAuthority(context.Background(), snaps, live)
	if err != nil {
		t.Fatalf("adoption with live authority = %v, want dispatchable", err)
	}
	if len(tasks) != 1 || tasks[0].Name != "worker" {
		t.Fatalf("tasks = %+v, want one task routed by handler worker", tasks)
	}

	// Without a live counterpart the rejection stays.
	_, _, err = c.tasksFromSnapshotsWithAuthority(context.Background(), snaps, nil)
	if err == nil || !strings.Contains(err.Error(), "no agent routing snapshot") {
		t.Fatalf("adoption without live authority = %v, want the routing-snapshot rejection", err)
	}

	// A live task whose handler disagrees with the snapshot must not launder
	// a different route through adoption.
	mismatched := []subagents.Task{{ID: "task-panel", Name: "other", Input: json.RawMessage(`"work"`)}}
	_, _, err = c.tasksFromSnapshotsWithAuthority(context.Background(), snaps, mismatched)
	if err == nil || !strings.Contains(err.Error(), "no agent routing snapshot") {
		t.Fatalf("adoption with mismatched live handler = %v, want rejection", err)
	}
}
