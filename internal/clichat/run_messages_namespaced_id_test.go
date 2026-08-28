package clichat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// TestRunMessagesReportsAndFiltersByNamespacedTaskID pins run_messages'
// side of the same fix TestSendToTaskResolvesNamespacedTaskID pins for
// send_to_task: a message posted from within a dispatch_tasks-spawned task
// is stamped with the task's REAL id (runtime.TaskIdentityFrom(ctx),
// namespaced - internal/cliorchestrate/dispatch.go's dispatchNamespace).
// run_messages must report that message's task_id back as the model's own
// raw id (not the internal namespaced form), and must resolve an incoming
// task_id FILTER supplied as the raw id against the same real task -
// exercised here on a SINGLE-task run, the shape
// commonTaskIDNamespace's old heuristic could never recover (it needed
// 2+ tasks to find a namespace boundary).
func TestRunMessagesReportsAndFiltersByNamespacedTaskID(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	_ = d.Register(runtime.Subagent, "finder", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		tool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		out, err := tool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"lock inversion at L42"}`))
		if err != nil {
			return nil, err
		}
		return json.RawMessage(out), nil
	}))
	pool := subagents.New(d, subagents.Policy{Workers: 1})
	c := coordinator.New(repo, pool)
	cliorchestrate.CoordinatorsForTest.Store(d, c)
	cliorchestrate.CoordinatorReposForTest.Store(d, repo)
	t.Cleanup(func() {
		cliorchestrate.CoordinatorsForTest.Delete(d)
		cliorchestrate.CoordinatorReposForTest.Delete(d)
	})

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "ns-solo:find-1", RawID: "find-1", Name: "finder", AgentName: "finder"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Results) != 1 || result.Results[0].Err != nil {
		t.Fatalf("result = %+v", result.Results)
	}
	runID := result.Snapshot.RunID
	cliorchestrate.StoreTestRunHandle(runID, c, h, repo, d, "sess-run-messages-ns")
	t.Cleanup(func() { cliorchestrate.RunHandlesForTest.Delete(runID) })
	ctx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-run-messages-ns"})

	tool := &runMessagesTool{dispatcher: d, cfg: cfg, repo: repo}

	// No filter: the returned message's task_id must be the raw id.
	out, err := tool.Execute(ctx, json.RawMessage(`{"run_id":"`+runID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"task_id":"find-1"`) {
		t.Errorf("run_messages output must report the model's raw id \"find-1\" verbatim, not a namespaced form: %s", out)
	}
	if strings.Contains(out, "ns-solo:find-1") {
		t.Errorf("run_messages output must not leak the internal namespaced id: %s", out)
	}

	// Filtered by the model's raw id: must still find the message (it was
	// stamped with the REAL namespaced id, not the raw one).
	filtered, err := tool.Execute(ctx, json.RawMessage(`{"run_id":"`+runID+`","task_id":"find-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Messages []struct {
			Synopsis string `json:"synopsis"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(filtered), &resp); err != nil {
		t.Fatalf("decode %q: %v", filtered, err)
	}
	if len(resp.Messages) != 1 || !strings.Contains(resp.Messages[0].Synopsis, "lock inversion") {
		t.Errorf("expected the finding filtered by raw task_id \"find-1\", got %s", filtered)
	}
}
