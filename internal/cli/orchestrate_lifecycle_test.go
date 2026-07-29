package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestSpawnAgentWaitRunReturnsTaskOutput(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"output":"completed analysis"}`), nil
	})); err != nil {
		t.Fatal(err)
	}

	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session-spawn", TurnID: "turn-1"})
	out, err := (&spawnAgentTool{dispatcher: dispatcher, cfg: config.DefaultSubagentConfig, repo: repo}).Execute(ctx, json.RawMessage(`{
		"tasks":[{"id":"t1","name":"oneshot","prompt":"analyze"}],"wait":"run"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		TaskResults []struct {
			TaskID string         `json:"task_id"`
			Output map[string]any `json:"output"`
		} `json:"task_results"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.TaskResults) != 1 || response.TaskResults[0].TaskID != "t1" || response.TaskResults[0].Output["output"] != "completed analysis" {
		t.Fatalf("task_results=%+v, want completed task output", response.TaskResults)
	}
}

func TestJoinRunTool_RecoveredRunUsesPersistedTaskResults(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	const runID = "cli-recovered-run"
	if err := repo.CreateRun(context.Background(), "cli-recovered", ledger.RunSnapshot{RunID: runID, Status: ledger.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}
	tasks := []ledger.TaskSnapshot{
		{RunID: runID, TaskID: "task-a", Status: string(ledger.TaskStatusCompleted), OutputRef: "ref:output:7", Version: 1,
			Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-a", TaskID: "task-a", RunID: runID, AttemptNum: 1, Status: string(ledger.TaskStatusCompleted)}}},
		{RunID: runID, TaskID: "task-b", Status: string(ledger.TaskStatusFailed), ErrorRef: "ref:error:deadbeef", Version: 1,
			Attempts: []ledger.AttemptSnapshot{{AttemptID: "attempt-b", TaskID: "task-b", RunID: runID, AttemptNum: 1, Status: string(ledger.TaskStatusFailed)}}},
	}
	for _, task := range tasks {
		if err := repo.CreateTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
	}
	c := coordinator.New(repo, subagents.New(runtime.New(runtime.Policy{}), subagents.Policy{Workers: 1}))
	d := runtime.New(runtime.Policy{})
	h, err := c.Spawn(context.Background(), nil, "cli-recovered")
	if err != nil {
		t.Fatal(err)
	}
	runHandles.Store(runID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: d, principal: orchestrationPrincipal{sessionID: "session-recovered"}})
	defer runHandles.Delete(runID)

	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "session-recovered"})
	out, err := (&joinRunTool{dispatcher: d, repo: repo}).Execute(ctx, json.RawMessage(`{"run_id":"cli-recovered-run"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		TaskResults []struct {
			TaskID    string `json:"task_id"`
			OutputRef string `json:"output_ref"`
			ErrorRef  string `json:"error_ref"`
		} `json:"task_results"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.TaskResults) != 2 {
		t.Fatalf("task results = %+v", response.TaskResults)
	}
	refs := make(map[string]struct{ output, failure string }, len(response.TaskResults))
	for _, task := range response.TaskResults {
		refs[task.TaskID] = struct{ output, failure string }{task.OutputRef, task.ErrorRef}
	}
	if refs["task-a"].output != "ref:output:7" {
		t.Fatalf("completed persisted result = %+v", refs["task-a"])
	}
	if refs["task-b"].failure != "ref:error:deadbeef" {
		t.Fatalf("failed persisted result = %+v", refs["task-b"])
	}
}

func TestOrchestrationLifecycleTools_RejectCrossSessionHandleAccess(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	origin := runtime.New(runtime.Policy{})
	other := runtime.New(runtime.Policy{})
	if err := repo.CreateRun(context.Background(), "cli-session-scoped", ledger.RunSnapshot{
		RunID: "run-session-scoped", Status: ledger.RunStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(origin, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), nil, "cli-session-scoped")
	if err != nil {
		t.Fatal(err)
	}
	const runID = "run-session-scoped"
	runHandles.Store(runID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: origin, principal: orchestrationPrincipal{sessionID: "origin"}})
	defer runHandles.Delete(runID)
	args := json.RawMessage(`{"run_id":"run-session-scoped"}`)

	tests := []struct {
		name string
		tool tools.Tool
	}{
		{name: "inspect", tool: &inspectAgentTool{dispatcher: other, repo: repo}},
		{name: "join", tool: &joinRunTool{dispatcher: other, repo: repo}},
		{name: "cancel", tool: &cancelRunTool{dispatcher: other, repo: repo}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "other"})
			out, err := tt.tool.Execute(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if out != `{"error":"unknown run_id"}` {
				t.Fatalf("cross-session access returned %q", out)
			}
		})
	}
}

// TestRunHandleNotAccessibleToOtherOwner reproduces the same-dispatcher IDOR:
// independent callers currently share a dispatcher and repository, so the
// lifecycle tools must not authorize solely on those two identities.
func TestRunHandleNotAccessibleToOtherOwner(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := repo.CreateRun(context.Background(), "cli-owner-scoped", ledger.RunSnapshot{
		RunID: "run-owner-scoped", Status: ledger.RunStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), nil, "cli-owner-scoped")
	if err != nil {
		t.Fatal(err)
	}
	const runID = "run-owner-scoped"
	runHandles.Store(runID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: dispatcher, principal: orchestrationPrincipal{sessionID: "owner-a"}})
	defer runHandles.Delete(runID)

	ownerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner-a"})
	otherCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner-b"})
	_ = ownerCtx
	for name, tool := range map[string]tools.Tool{
		"inspect": &inspectAgentTool{dispatcher: dispatcher, repo: repo},
		"join":    &joinRunTool{dispatcher: dispatcher, repo: repo},
		"cancel":  &cancelRunTool{dispatcher: dispatcher, repo: repo},
	} {
		out, err := tool.Execute(otherCtx, json.RawMessage(`{"run_id":"run-owner-scoped"}`))
		if err != nil {
			t.Fatal(err)
		}
		if out != `{"error":"unknown run_id"}` {
			t.Fatalf("foreign owner %s accessed run: %s", name, out)
		}
	}
}

func TestCancelRunCannotCancelForeignRun(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	started := make(chan struct{})
	release := make(chan struct{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ownerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "owner"})
	spawn := &spawnAgentTool{dispatcher: dispatcher, cfg: config.DefaultSubagentConfig, repo: repo}
	out, err := spawn.Execute(ownerCtx, json.RawMessage(`{"tasks":[{"id":"t1","name":"oneshot","prompt":"work"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatal(err)
	}
	<-started
	foreignCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "other"})
	cancelOut, err := (&cancelRunTool{dispatcher: dispatcher, repo: repo}).Execute(foreignCtx, json.RawMessage(`{"run_id":"`+response.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cancelOut != `{"error":"unknown run_id"}` {
		t.Fatalf("foreign cancel response = %s", cancelOut)
	}
	close(release)
	joined, err := (&joinRunTool{dispatcher: dispatcher, repo: repo}).Execute(ownerCtx, json.RawMessage(`{"run_id":"`+response.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joined, `"status":"completed"`) {
		t.Fatalf("foreign cancel affected run: %s", joined)
	}
}

func TestDispatcherCloseUnregistersOrchestrationHandle(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "oneshot"}}, "close-unregister")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	record := &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: dispatcher, principal: orchestrationPrincipal{sessionID: "session"}, retention: time.Hour}
	storeOrchestrationHandle(snap.RunID, record)
	dispatcher.Close()
	if _, ok := runHandles.Load(snap.RunID); ok {
		t.Fatal("dispatcher close retained orchestration handle")
	}
}

func TestRunHandleAccessibleToAncestor(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "oneshot"}}, "ancestor-access")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	runHandles.Store(snap.RunID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: dispatcher, principal: orchestrationPrincipal{sessionID: "shared-session"}})
	defer runHandles.Delete(snap.RunID)

	parentCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "shared-session"})
	out, err := (&inspectAgentTool{dispatcher: dispatcher, repo: repo}).Execute(parentCtx, json.RawMessage(`{"run_id":"`+snap.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out == `{"error":"unknown run_id"}` {
		t.Fatal("parent could not access child run")
	}
}

func TestUnauthorizedAndUnknownAreIndistinguishable(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{})
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })); err != nil {
		t.Fatal(err)
	}
	c := coordinator.New(repo, subagents.New(dispatcher, subagents.Policy{Workers: 1}))
	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "oneshot"}}, "indistinguishable")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Inspect(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	runHandles.Store(snap.RunID, &orchestrationHandle{coord: c, handle: h, repo: repo, dispatcher: dispatcher, principal: orchestrationPrincipal{sessionID: "owner"}})
	defer runHandles.Delete(snap.RunID)
	foreignCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "other"})
	tool := &inspectAgentTool{dispatcher: dispatcher, repo: repo}
	unauthorized, err := tool.Execute(foreignCtx, json.RawMessage(`{"run_id":"`+snap.RunID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := tool.Execute(foreignCtx, json.RawMessage(`{"run_id":"run-does-not-exist"}`))
	if err != nil {
		t.Fatal(err)
	}
	if unauthorized != unknown || unknown != `{"error":"unknown run_id"}` {
		t.Fatalf("unauthorized=%q unknown=%q", unauthorized, unknown)
	}
}

func TestTaskDepthPropagates(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	dispatcher := runtime.New(runtime.Policy{MaxDepth: 8})
	depths := make(chan int, 1)
	if err := dispatcher.Register(runtime.Subagent, "oneshot", handlerFunc(func(_ context.Context, req runtime.Request) (json.RawMessage, error) {
		depths <- req.Depth
		return json.RawMessage(`{}`), nil
	})); err != nil {
		t.Fatal(err)
	}
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "depth-session", TurnID: "turn-1", Depth: 1})
	_, err := (&spawnAgentTool{dispatcher: dispatcher, cfg: config.DefaultSubagentConfig, repo: repo}).Execute(ctx, json.RawMessage(`{"tasks":[{"id":"t1","name":"oneshot","prompt":"work"}],"wait":"run"}`))
	if err != nil {
		t.Fatal(err)
	}
	if depth := <-depths; depth != 2 {
		t.Fatalf("task depth = %d, want 2", depth)
	}
}
