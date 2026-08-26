package coordinator

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// toolCallEmittingHandler pulls the ToolCallSink off the task's own context
// (installed by contextForTask via runToolCallBuffer, not hand-rolled) and
// invokes it directly with distinguishable steps. This exercises the REAL
// per-task context path end to end: pool -> contextForTask -> sinkFor ->
// buffer -> recordTaskResult's flush -> ledger.
type toolCallEmittingHandler struct {
	toolName string
}

func (h toolCallEmittingHandler) Invoke(ctx context.Context, req runtime.Request) (json.RawMessage, error) {
	sink, ok := subagents.ToolCallSinkFrom(ctx)
	if !ok {
		return nil, errNoSinkOnContext
	}
	sink(subagents.ToolCallStep{ToolCallID: h.toolName + "-call-1", Name: h.toolName, Kind: "start", Input: h.toolName + "-input"})
	sink(subagents.ToolCallStep{ToolCallID: h.toolName + "-call-1", Name: h.toolName, Kind: "end", Output: h.toolName + "-output"})
	return json.RawMessage(`{"ok":true}`), nil
}

var errNoSinkOnContext = &noSinkError{}

type noSinkError struct{}

func (*noSinkError) Error() string { return "no ToolCallSink on task context" }

// TestRecordTaskResultFlushesToolCallsToLedger is the end-to-end integration
// test for Part B chunk 4: a task dispatched through the real coordinator ->
// pool -> contextForTask path, whose handler emits tool-call steps via the
// sink pulled off its OWN context, must have those steps persisted to the
// ledger as a ToolCallsRef the coordinator can resolve back to the original
// steps (including ToolCallID).
func TestRecordTaskResultFlushesToolCallsToLedger(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "toolcaller", toolCallEmittingHandler{toolName: "read_file"}); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 1})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "t1", Name: "toolcaller"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	snap, err := repo.GetTask(context.Background(), h.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if snap.ToolCallsRef == "" {
		t.Fatal("expected non-empty ToolCallsRef after task completion")
	}

	data, err := repo.LoadContent(context.Background(), snap.ToolCallsRef)
	if err != nil {
		t.Fatalf("LoadContent(%q): %v", snap.ToolCallsRef, err)
	}
	var steps []subagents.ToolCallStep
	if err := json.Unmarshal(data, &steps); err != nil {
		t.Fatalf("unmarshal steps: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2: %+v", len(steps), steps)
	}
	if steps[0].Kind != "start" || steps[0].ToolCallID != "read_file-call-1" || steps[0].Name != "read_file" {
		t.Fatalf("steps[0] = %+v, want start/read_file-call-1/read_file", steps[0])
	}
	if steps[1].Kind != "end" || steps[1].ToolCallID != "read_file-call-1" {
		t.Fatalf("steps[1] = %+v, want end/read_file-call-1", steps[1])
	}
}

// TestRecordTaskResultToolCallsIsolatedAcrossConcurrentTasks dispatches two
// tasks in the same run batch, each emitting distinct, task-identifiable
// tool-call steps. Their stored refs must never cross-contaminate. Run under
// -race.
func TestRecordTaskResultToolCallsIsolatedAcrossConcurrentTasks(t *testing.T) {
	repo := ledger.NewMemoryLedgerRepository()
	d := runtime.New(runtime.Policy{})
	if err := d.Register(runtime.Subagent, "toolcaller-a", toolCallEmittingHandler{toolName: "tool_alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Register(runtime.Subagent, "toolcaller-b", toolCallEmittingHandler{toolName: "tool_beta"}); err != nil {
		t.Fatal(err)
	}
	p := subagents.New(d, subagents.Policy{Workers: 4})
	c := New(repo, p)

	h, err := c.Spawn(context.Background(), []subagents.Task{
		{ID: "ta", Name: "toolcaller-a"},
		{ID: "tb", Name: "toolcaller-b"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Join(context.Background(), h); err != nil {
		t.Fatal(err)
	}

	snapA, err := repo.GetTask(context.Background(), h.runID, "ta")
	if err != nil {
		t.Fatal(err)
	}
	snapB, err := repo.GetTask(context.Background(), h.runID, "tb")
	if err != nil {
		t.Fatal(err)
	}
	if snapA.ToolCallsRef == "" || snapB.ToolCallsRef == "" {
		t.Fatalf("expected both refs non-empty: a=%q b=%q", snapA.ToolCallsRef, snapB.ToolCallsRef)
	}
	if snapA.ToolCallsRef == snapB.ToolCallsRef {
		t.Fatalf("task A and B share the same ToolCallsRef: %q", snapA.ToolCallsRef)
	}

	dataA, err := repo.LoadContent(context.Background(), snapA.ToolCallsRef)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := repo.LoadContent(context.Background(), snapB.ToolCallsRef)
	if err != nil {
		t.Fatal(err)
	}

	var stepsA, stepsB []subagents.ToolCallStep
	if err := json.Unmarshal(dataA, &stepsA); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dataB, &stepsB); err != nil {
		t.Fatal(err)
	}

	for _, s := range stepsA {
		if s.Name != "tool_alpha" {
			t.Fatalf("task A ref contains a non-alpha step: %+v", s)
		}
		if s.Name == "tool_beta" || s.Input == "tool_beta-input" || s.Output == "tool_beta-output" {
			t.Fatalf("task A ref contaminated with B content: %+v", s)
		}
	}
	for _, s := range stepsB {
		if s.Name != "tool_beta" {
			t.Fatalf("task B ref contains a non-beta step: %+v", s)
		}
		if s.Name == "tool_alpha" || s.Input == "tool_alpha-input" || s.Output == "tool_alpha-output" {
			t.Fatalf("task B ref contaminated with A content: %+v", s)
		}
	}
}
