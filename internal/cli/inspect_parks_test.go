package cli

// E6 observability: inspect_agents surfaces live parked questions as a "parks"
// field (TaskID/MessageID/ExpiresAt), and renders an empty list when none.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// spawnInspectRun spawns one fast worker and returns the dispatcher,
// coordinator, repo, handle, and snapshot with a stored orchestration handle
// accessible to the "session" caller. The parked-asker fixture is the live run
// itself: the test parks a question on the run's task via the coordinator
// before inspecting.
func spawnInspectRun(t *testing.T) (*runtime.Dispatcher, coordinator.Coordinator, ledger.LedgerRepository, *coordinator.RunHandle, ledger.RunSnapshot) {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	_ = dispatcher.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}))
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "worker"}}, "inspect-parks-run")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	runHandles.Store(snap.RunID, &orchestrationHandle{
		coord: c, handle: h, repo: repo, dispatcher: dispatcher,
		principal: orchestrationPrincipal{sessionID: "session"},
	})
	t.Cleanup(func() { runHandles.Delete(snap.RunID) })
	return dispatcher, c, repo, h, snap
}

func inspectWithOwner(t *testing.T, repo ledger.LedgerRepository, dispatcher *runtime.Dispatcher, runID string) string {
	t.Helper()
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session"})
	out, err := (&inspectAgentTool{dispatcher: dispatcher, repo: repo}).Execute(ctx, json.RawMessage(`{"run_id":"`+runID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out == `{"error":"unknown run_id"}` {
		t.Fatal("owner could not access own run")
	}
	return out
}

// TestInspectAgentsIncludesParks: a run with a parked asker surfaces it as a
// "parks" entry carrying task_id, message_id, and expires_at.
func TestInspectAgentsIncludesParks(t *testing.T) {
	dispatcher, c, repo, _, snap := spawnInspectRun(t)
	taskID := snap.Tasks[0].TaskID
	_, unpark, err := c.ParkQuestion(snap.RunID, taskID, "ask-parked")
	if err != nil {
		t.Fatal(err)
	}
	defer unpark()

	out := inspectWithOwner(t, repo, dispatcher, snap.RunID)

	var res struct {
		Parks []struct {
			TaskID    string `json:"task_id"`
			MessageID string `json:"message_id"`
			ExpiresAt string `json:"expires_at"`
		} `json:"parks"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Parks) != 1 {
		t.Fatalf("parks = %+v, want 1", res.Parks)
	}
	p := res.Parks[0]
	if p.TaskID != taskID || p.MessageID != "ask-parked" {
		t.Fatalf("park = %+v, want task %q ask ask-parked", p, taskID)
	}
	if p.ExpiresAt == "" {
		t.Fatal("park must include expires_at")
	}
}

// TestInspectAgentsParksEmptyWhenNone: no parked questions → "parks": [].
func TestInspectAgentsParksEmptyWhenNone(t *testing.T) {
	dispatcher, _, repo, _, snap := spawnInspectRun(t)
	out := inspectWithOwner(t, repo, dispatcher, snap.RunID)

	var res struct {
		Parks []json.RawMessage `json:"parks"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res.Parks == nil {
		t.Fatalf("parks must be present as an empty list: %s", out)
	}
	if len(res.Parks) != 0 {
		t.Fatalf("parks = %+v, want empty list", res.Parks)
	}
}
