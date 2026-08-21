package ledger_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

type recordingEngine struct {
	started []ledger.StartRequest
}

func (e *recordingEngine) Start(_ context.Context, req ledger.StartRequest) (ledger.StartResult, error) {
	e.started = append(e.started, req)
	return ledger.StartResult{RunID: "wfr-rec", Status: "running"}, nil
}
func (e *recordingEngine) Cancel(context.Context, string) (ledger.CancelResult, error) {
	return ledger.CancelResult{}, nil
}
func (e *recordingEngine) Deliver(context.Context, string, bool) (ledger.DeliverResult, error) {
	return ledger.DeliverResult{}, nil
}
func (e *recordingEngine) Delete(context.Context, string, bool) (ledger.DeleteResult, error) {
	return ledger.DeleteResult{}, nil
}

// TestRunToolPreservesLargeIntegerInputs pins that workflow_run decodes inputs
// with UseNumber: an integer ≥ 2^53 must reach the engine verbatim. The plain
// float64 decode rounded it, so the admitted run executed with different input
// than requested (silent corruption).
func TestRunToolPreservesLargeIntegerInputs(t *testing.T) {
	engine := &recordingEngine{}
	svc := testService(t, ledger.NewMemoryRepository(), engine)
	if _, err := findTool(t, svc, ledger.ToolWorkflowRun).Execute(
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
