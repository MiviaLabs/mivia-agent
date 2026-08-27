package cliorchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// dispatchRunPayload runs dispatch_tasks with wait=task and returns the decoded payload.
// It fails the test if Execute returns a Go error: a run outcome must never travel
// that way. runtime.Dispatcher.failResult replaces a failed tool's Output with
// exactly {"status":"failed"} - pinned by TestDispatcherFailUsesBoundedReferences,
// which asserts the error text does not survive - so an error return would destroy
// the payload AND the message, leaving the model with seven words.
func dispatchRunPayload(t *testing.T, tasksJSON string) map[string]any {
	t.Helper()
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "fail", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return nil, errors.New("intentional failure")
	})); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(runtime.Subagent, "ok", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "run-error-test", TurnID: "turn-1"})

	out, err := (&dispatchTasksTool{
		dispatcher: dispatcher, cfg: config.DefaultSubagentConfig, repo: repo, agentReg: testAgentRegistry(t, "fail", "ok"),
	}).Execute(ctx, json.RawMessage(tasksJSON))
	if err != nil {
		t.Fatalf("dispatch_tasks must report run outcomes in the payload, not as a Go error: %v", err)
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &payload); jsonErr != nil {
		t.Fatalf("payload is not JSON: %v\n%s", jsonErr, out)
	}
	return payload
}

// TestDispatchTasksReportsBlockedDependency asserts that when waiting on a task
// blocked by a failed dependency, the payload carries run_id and status so the model
// can inspect, join or cancel the run.
func TestDispatchTasksReportsBlockedDependency(t *testing.T) {
	payload := dispatchRunPayload(t, `{
		"tasks":[
			{"id":"parent","agent":"fail","prompt":"boom"},
			{"id":"child","agent":"fail","prompt":"never runs","depends_on":["parent"]}
		],"wait":"task","wait_task_id":"child"
	}`)

	if payload["run_id"] == "" || payload["run_id"] == nil {
		t.Error("run_id must survive a failed run, or the model cannot reach it again")
	}
	if payload["status"] != "failed" {
		t.Errorf("status = %v, want failed", payload["status"])
	}
}

// TestSpawnResultPayloadCarriesRunError pins the plumbing independently of which
// failure produced it, which is the wider point of the fix: waitForSpawnResult
// discarded RunResult.Err outright, so every run-level failure the DAG can join -
// retry exhaustion (dag.go "retry exhausted (run ended)"), a missing task result
// (record_results.go), and any ledger persistence error - reached the model as a
// payload with no explanation in it at all.
//
// Malformed graphs are not in this set: coordinator/validation.go rejects a cycle
// or an unknown dependency at Spawn time, before a run exists.
func TestSpawnResultPayloadCarriesRunError(t *testing.T) {
	snap := ledger.RunSnapshot{RunID: "run-x", DisplayName: "audit", Status: "failed"}
	completed := &coordinator.RunResult{
		Snapshot: snap,
		Err:      errors.New("retry exhausted (run ended)"),
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(spawnResultPayload(snap, completed, 4096, nil)), &payload); err != nil {
		t.Fatal(err)
	}
	if got, _ := payload["run_error"].(string); !strings.Contains(got, "retry exhausted") {
		t.Errorf("run_error = %q, want the joined run failure", got)
	}
	if payload["run_id"] != "run-x" {
		t.Errorf("run_id = %v, want run-x", payload["run_id"])
	}
}

// TestDispatchTasksSuccessLeavesRunErrorEmpty - clean run does not report run_error.
func TestDispatchTasksSuccessLeavesRunErrorEmpty(t *testing.T) {
	payload := dispatchRunPayload(t, `{
		"tasks":[{"id":"solo","agent":"ok","prompt":"x"}],"wait":"none"
	}`)
	if runErr, _ := payload["run_error"].(string); runErr != "" {
		t.Errorf("clean run reported run_error = %q", runErr)
	}
}
