package cliorchestrate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// TestJoinRunAndInspectAgentsStripNamespaceFromTaskIDs pins a real,
// reachable bug: dispatch_tasks mints each task's real internal id as
// namespace+":"+rawID (dispatchNamespace/namespacedTaskID) and strips that
// prefix from its OWN model-visible output (stripNamespace), but join_run
// and inspect_agents - separate tools that only ever see a run_id, never
// the dispatch call that minted the run - reported the raw ledger
// TaskSnapshot.TaskID verbatim, leaking the internal namespaced form (e.g.
// "call_xyz:t1" instead of "t1") to the model whenever it dispatched with
// wait="none" (the tool's own recommended pattern for interactive
// sessions) and later inspected or joined the run.
func TestJoinRunAndInspectAgentsStripNamespaceFromTaskIDs(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	release := make(chan struct{})
	if err := dispatcher.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}

	callerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-join-inspect-ns"})
	tool := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "worker"))
	out, err := tool.Execute(callerCtx, json.RawMessage(
		`{"tasks":[{"id":"t1","agent":"worker","prompt":"a"},{"id":"t2","agent":"worker","prompt":"b"}],"wait":"none"}`))
	if err != nil {
		t.Fatal(err)
	}
	var dispatchResp struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(out), &dispatchResp); err != nil {
		t.Fatalf("decode dispatch response %q: %v", out, err)
	}
	<-started
	<-started

	inspectOut, err := (&inspectAgentTool{dispatcher: dispatcher, repo: repo}).Execute(callerCtx,
		json.RawMessage(`{"run_id":"`+dispatchResp.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"t1", "t2"} {
		if !strings.Contains(inspectOut, `"task_id":"`+raw+`"`) {
			t.Errorf("inspect_agents output must report the model's raw id %q verbatim, not a namespaced form: %s", raw, inspectOut)
		}
	}

	close(release)
	joinOut, err := (&joinRunTool{dispatcher: dispatcher, repo: repo}).Execute(callerCtx,
		json.RawMessage(`{"run_id":"`+dispatchResp.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"t1", "t2"} {
		if !strings.Contains(joinOut, `"task_id":"`+raw+`"`) {
			t.Errorf("join_run output must report the model's raw id %q verbatim, not a namespaced form: %s", raw, joinOut)
		}
	}
}
