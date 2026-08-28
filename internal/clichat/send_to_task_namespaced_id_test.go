package clichat

import (
	"encoding/json"
	"testing"
)

// TestSendToTaskResolvesNamespacedTaskID pins the fix for a real,
// reachable bug: dispatch_tasks mints each task's real internal id as
// namespace+":"+rawID (internal/cliorchestrate/dispatch.go's
// dispatchNamespace/namespacedTaskID) but strips that prefix from every
// model-visible surface, so the model only ever learns its own raw id
// (e.g. "t1"). Before resolveSendTargetTaskID, send_to_task passed that
// raw id straight through to the coordinator, which lazily creates a
// fresh, empty mailbox for whatever key it's given - so the call reported
// delivered:true while the message was silently orphaned, never reaching
// the live task (real id "ns1:t1"). Simulates the namespaced-admission
// shape directly (spawnBroadcastRun takes the real Task.ID verbatim, the
// same shape dispatch_tasks' buildTasks produces) without needing the
// full dispatch_tasks Execute pipeline.
func TestSendToTaskResolvesNamespacedTaskID(t *testing.T) {
	run := spawnBroadcastRun(t, map[string]bool{"ns1:t1": true}, "ns1:t1")
	defer joinReleased(t, run)
	run.waitStarted(t, "ns1:t1")

	out, err := run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_id":"t1",
		"kind":"steer",
		"body":"go faster"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Delivered bool `json:"delivered"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if !resp.Delivered {
		t.Fatalf("expected delivered=true: the model's raw id %q must resolve to the real task %q, got %s", "t1", "ns1:t1", out)
	}
}

// TestSendToTaskBroadcastResolvesNamespacedTaskIDs is the broadcast-path
// sibling of TestSendToTaskResolvesNamespacedTaskID: task_ids must resolve
// the same way per-target, and the result map must stay keyed by the
// model's own raw ids (not the resolved real ones), so the model can
// still correlate each result with the id it requested.
func TestSendToTaskBroadcastResolvesNamespacedTaskIDs(t *testing.T) {
	run := spawnBroadcastRun(t,
		map[string]bool{"ns2:a": true, "ns2:b": true}, "ns2:a", "ns2:b")
	defer joinReleased(t, run)
	run.waitStarted(t, "ns2:a")
	run.waitStarted(t, "ns2:b")

	out, err := run.tool.Execute(run.ctx, json.RawMessage(`{
		"run_id":"`+run.runID+`",
		"task_ids":["a","b"],
		"kind":"steer",
		"body":"go faster"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Results map[string]struct {
			Delivered bool   `json:"delivered"`
			Error     string `json:"error,omitempty"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	for _, raw := range []string{"a", "b"} {
		r, ok := resp.Results[raw]
		if !ok {
			t.Fatalf("expected a result keyed by the model's raw id %q, got %+v", raw, resp.Results)
		}
		if !r.Delivered {
			t.Errorf("task %q: expected delivered=true, got %+v", raw, r)
		}
	}
}
