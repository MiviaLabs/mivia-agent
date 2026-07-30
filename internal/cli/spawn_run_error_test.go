package cli

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

// spawnRunPayload runs spawn_agent with wait=run and returns the decoded payload.
// It fails the test if Execute returns a Go error: a run outcome must never travel
// that way. runtime.Dispatcher.failResult replaces a failed tool's Output with
// exactly {"status":"failed"} — pinned by TestDispatcherFailUsesBoundedReferences,
// which asserts the error text does not survive — so an error return would destroy
// the payload AND the message, leaving the model with seven words.
func spawnRunPayload(t *testing.T, tasksJSON string) map[string]any {
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

	out, err := (&spawnAgentTool{
		dispatcher: dispatcher, cfg: config.DefaultSubagentConfig, repo: repo,
	}).Execute(ctx, json.RawMessage(tasksJSON))
	if err != nil {
		t.Fatalf("spawn_agent must report run outcomes in the payload, not as a Go error: %v", err)
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &payload); jsonErr != nil {
		t.Fatalf("payload is not JSON: %v\n%s", jsonErr, out)
	}
	return payload
}

// TestSpawnAgentReportsBlockedDependency replaces an earlier test that asserted
// Execute returns a Go error naming the blocked dependency. That expectation is
// self-defeating: the dispatcher strips a failed tool's output down to
// {"status":"failed"}, so the model would receive neither the payload nor the
// error. The observable contract is the payload, and it must carry the failure
// alongside run_id so the model can still inspect, join or cancel the run.
func TestSpawnAgentReportsBlockedDependency(t *testing.T) {
	payload := spawnRunPayload(t, `{
		"tasks":[
			{"id":"parent","name":"fail","prompt":"boom"},
			{"id":"child","name":"fail","prompt":"never runs","depends_on":["parent"]}
		],"wait":"run"
	}`)

	runErr, _ := payload["run_error"].(string)
	if runErr == "" {
		t.Fatal("a run blocked by a failed dependency must report run_error")
	}
	for _, want := range []string{"child", "parent"} {
		if !strings.Contains(runErr, want) {
			t.Errorf("run_error %q must name %q", runErr, want)
		}
	}
	if payload["run_id"] == "" || payload["run_id"] == nil {
		t.Error("run_id must survive a failed run, or the model cannot reach it again")
	}
	if payload["task_results"] == nil {
		t.Error("task_results must survive a failed run")
	}
	if payload["status"] != "failed" {
		t.Errorf("status = %v, want failed", payload["status"])
	}
}

// TestSpawnResultPayloadCarriesRunError pins the plumbing independently of which
// failure produced it, which is the wider point of the fix: waitForSpawnResult
// discarded RunResult.Err outright, so every run-level failure the DAG can join —
// retry exhaustion (dag.go "retry exhausted (run ended)"), a missing task result
// (record_results.go), and any ledger persistence error — reached the model as a
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
	if err := json.Unmarshal([]byte(spawnResultPayload(snap, completed)), &payload); err != nil {
		t.Fatal(err)
	}
	if got, _ := payload["run_error"].(string); !strings.Contains(got, "retry exhausted") {
		t.Errorf("run_error = %q, want the joined run failure", got)
	}
	if payload["run_id"] != "run-x" {
		t.Errorf("run_id = %v, want run-x", payload["run_id"])
	}
}

// TestSpawnAgentSuccessLeavesRunErrorEmpty - the new field must not imply failure
// on a clean run.
func TestSpawnAgentSuccessLeavesRunErrorEmpty(t *testing.T) {
	payload := spawnRunPayload(t, `{
		"tasks":[{"id":"solo","name":"ok","prompt":"x"}],"wait":"run"
	}`)
	if runErr, _ := payload["run_error"].(string); runErr != "" {
		t.Errorf("clean run reported run_error = %q", runErr)
	}
}
