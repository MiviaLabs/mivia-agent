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

// TestJoinRunAndInspectAgentsStripNamespaceFromSingleTaskRun is the
// single-task sibling of TestJoinRunAndInspectAgentsStripNamespaceFromTaskIDs:
// commonTaskIDNamespace's longest-common-prefix heuristic needs at least 2
// tasks in a run to disambiguate "namespace:rawid" from a raw id that
// itself happens to contain a colon, so a dispatch_tasks(wait="none") call
// with exactly one task - a common, realistic shape - still leaked the
// internal namespaced id through join_run/inspect_agents even after the
// multi-task case was fixed. RawID closes this: it needs no comparison
// across tasks at all.
func TestJoinRunAndInspectAgentsStripNamespaceFromSingleTaskRun(t *testing.T) {
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

	callerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-join-inspect-ns-solo"})
	tool := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "worker"))
	out, err := tool.Execute(callerCtx, json.RawMessage(
		`{"tasks":[{"id":"solo","agent":"worker","prompt":"a"}],"wait":"none"}`))
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

	inspectOut, err := (&inspectAgentTool{dispatcher: dispatcher, repo: repo}).Execute(callerCtx,
		json.RawMessage(`{"run_id":"`+dispatchResp.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inspectOut, `"task_id":"solo"`) {
		t.Errorf("inspect_agents output must report the model's raw id \"solo\" verbatim on a single-task run, not a namespaced form: %s", inspectOut)
	}

	close(release)
	joinOut, err := (&joinRunTool{dispatcher: dispatcher, repo: repo}).Execute(callerCtx,
		json.RawMessage(`{"run_id":"`+dispatchResp.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joinOut, `"task_id":"solo"`) {
		t.Errorf("join_run output must report the model's raw id \"solo\" verbatim on a single-task run, not a namespaced form: %s", joinOut)
	}
}

func TestStripNamespaceAndResolveTaskIDEdgeCases(t *testing.T) {
	if got := stripNamespace("", "foo:bar"); got != "foo:bar" {
		t.Errorf("stripNamespace empty namespace: got %q, want foo:bar", got)
	}
	tasks := []ledger.TaskSnapshot{
		{TaskID: "call-1:t1", RawID: "t1"},
	}
	if got := ResolveTaskID(tasks, "unknown"); got != "unknown" {
		t.Errorf("ResolveTaskID unknown: got %q, want unknown", got)
	}
}

func TestDispatchTasksSpawnAndWaitError(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	callerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-err"})
	tool := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "worker"))
	_, err := tool.Execute(callerCtx, json.RawMessage(
		`{"tasks":[{"id":"t1","agent":"worker","prompt":"a"}],"wait":"task","wait_task_id":"missing"}`))
	if err == nil {
		t.Fatal("expected error for missing wait_task_id")
	}
}

func TestInspectAgentsResolvesDependsOn(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	_ = dispatcher.Register(runtime.Subagent, "worker", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	}))
	callerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-dep"})
	tool := NewDispatchTasksToolConfigured(dispatcher, config.DefaultSubagentConfig, repo, testAgentRegistry(t, "worker"))
	out, err := tool.Execute(callerCtx, json.RawMessage(
		`{"tasks":[{"id":"t1","agent":"worker","prompt":"a"},{"id":"t2","agent":"worker","prompt":"b","depends_on":["t1"]}],"wait":"none"}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		RunID string `json:"run_id"`
	}
	_ = json.Unmarshal([]byte(out), &resp)
	inspectOut, err := (&inspectAgentTool{dispatcher: dispatcher, repo: repo}).Execute(callerCtx,
		json.RawMessage(`{"run_id":"`+resp.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inspectOut, `"depends_on":["t1"]`) {
		t.Errorf("expected depends_on to contain t1, got %s", inspectOut)
	}
}
