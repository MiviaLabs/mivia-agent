package agenttools_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type recordingEngine struct {
	started []agenttools.StartRequest
}

func (e *recordingEngine) Start(_ context.Context, req agenttools.StartRequest) (agenttools.StartResult, error) {
	e.started = append(e.started, req)
	return agenttools.StartResult{RunID: "wfr-rec", Status: "running"}, nil
}
func (e *recordingEngine) Cancel(context.Context, string) (agenttools.CancelResult, error) {
	return agenttools.CancelResult{}, nil
}
func (e *recordingEngine) Deliver(context.Context, string, bool) (agenttools.DeliverResult, error) {
	return agenttools.DeliverResult{}, nil
}

// TestRunToolPreservesLargeIntegerInputs pins that workflow_run decodes inputs
// with UseNumber: an integer ≥ 2^53 must reach the engine verbatim. The plain
// float64 decode rounded it, so the admitted run executed with different input
// than requested (silent corruption).
func TestRunToolPreservesLargeIntegerInputs(t *testing.T) {
	engine := &recordingEngine{}
	svc := testService(t, workflowledger.NewMemoryRepository(), engine)
	if _, err := findTool(t, svc, agenttools.ToolWorkflowRun).Execute(
		context.Background(), json.RawMessage(`{"workflow":"w","inputs":{"n":9007199254740993}}`)); err != nil {
		t.Fatal(err)
	}
	if len(engine.started) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(engine.started))
	}
	n, ok := engine.started[0].Inputs["n"].(json.Number)
	if !ok {
		t.Fatalf("input n = %T (%v), want json.Number", engine.started[0].Inputs["n"], engine.started[0].Inputs["n"])
	}
	if n.String() != "9007199254740993" {
		t.Fatalf("input n = %s, want 9007199254740993", n.String())
	}
}
