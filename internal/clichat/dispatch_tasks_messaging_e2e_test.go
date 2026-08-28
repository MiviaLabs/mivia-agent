package clichat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// TestDispatchTasksThenSendToTaskAndRunMessagesResolveRawID is the real-
// Execute-path sibling of TestSendToTaskResolvesNamespacedTaskID /
// TestRunMessagesReportsAndFiltersByNamespacedTaskID: those hand-construct
// subagents.Task{ID, RawID} directly, so a defect inside dispatch_tasks'
// own Execute -> buildTasks (a wrong RawID value, a mismatch between the
// InvocationKey/ID/RawID namespace formula) would not be caught by them -
// only by a test that drives the real dispatchTasksTool.Execute the way a
// model actually would. This test dispatches with wait="none" (single
// task, the shape the namespace heuristic this whole feature replaced
// could never recover), lets the live handler post a message and park a
// question using the model's own raw id, then drives send_to_task and
// run_messages against the resulting run exactly as a model would after
// reading dispatch_tasks' own (already-raw) response.
func TestDispatchTasksThenSendToTaskAndRunMessagesResolveRawID(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	cfg := config.DefaultSubagentConfig
	started := make(chan struct{})
	release := make(chan struct{})
	if err := d.Register(runtime.Subagent, "worker", handlerFunc(func(ctx context.Context, _ runtime.Request) (json.RawMessage, error) {
		msgTool := &postMessageTool{dispatcher: d, cfg: cfg, repo: repo}
		if _, err := msgTool.Execute(ctx, json.RawMessage(`{"kind":"finding","body":"lock inversion at L42"}`)); err != nil {
			return nil, err
		}
		close(started)
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatal(err)
	}

	callerCtx := runtime.ContextWithCaller(context.Background(), runtime.Caller{SessionID: "sess-dispatch-then-message"})
	dispatchTool := cliorchestrate.NewDispatchTasksToolConfigured(d, cfg, repo, testAgentRegistry(t, "worker"))
	out, err := dispatchTool.Execute(callerCtx, json.RawMessage(
		`{"tasks":[{"id":"solo","agent":"worker","prompt":"investigate"}],"wait":"none"}`))
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

	// run_messages: the model's raw id "solo" must both filter correctly
	// and be reported back as "solo" (never a namespaced form) in the
	// result, using the SAME run_id and SAME raw id dispatch_tasks itself
	// just handed the model.
	runMsgTool := &runMessagesTool{dispatcher: d, cfg: cfg, repo: repo}
	msgsOut, err := runMsgTool.Execute(callerCtx, json.RawMessage(`{"run_id":"`+dispatchResp.RunID+`","task_id":"solo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msgsOut, `"task_id":"solo"`) {
		t.Errorf("run_messages must report the raw id \"solo\" after a real dispatch_tasks call, got %s", msgsOut)
	}
	if !strings.Contains(msgsOut, "lock inversion") {
		t.Errorf("run_messages filtered by raw id \"solo\" must find the finding it posted, got %s", msgsOut)
	}

	// send_to_task: steering by the model's raw id must reach the live
	// task (real id "<callID>:solo"), not silently land in an orphaned
	// mailbox keyed by the raw id alone.
	sendTool := &sendToTaskTool{dispatcher: d, cfg: cfg, repo: repo}
	sendOut, err := sendTool.Execute(callerCtx, json.RawMessage(`{
		"run_id":"`+dispatchResp.RunID+`",
		"task_id":"solo",
		"kind":"steer",
		"body":"keep going"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var sendResp struct {
		Delivered bool `json:"delivered"`
	}
	if err := json.Unmarshal([]byte(sendOut), &sendResp); err != nil {
		t.Fatalf("decode send_to_task response %q: %v", sendOut, err)
	}
	if !sendResp.Delivered {
		t.Fatalf("send_to_task must resolve the raw id \"solo\" to the live task after a real dispatch_tasks call, got %s", sendOut)
	}
	close(release)
}
