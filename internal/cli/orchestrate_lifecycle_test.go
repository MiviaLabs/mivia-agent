package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

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
	h, err := c.Spawn(context.Background(), nil, "cli-recovered")
	if err != nil {
		t.Fatal(err)
	}
	runHandles.Store(runID, &orchestrationHandle{coord: c, handle: h, repo: repo})
	defer runHandles.Delete(runID)

	out, err := (&joinRunTool{repo: repo}).Execute(context.Background(), json.RawMessage(`{"run_id":"cli-recovered-run"}`))
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
