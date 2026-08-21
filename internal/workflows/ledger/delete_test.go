package ledger_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// deleteEngine records Delete calls so service/tool tests can assert the
// run_id (and force flag) reach the engine untouched.
type deleteEngine struct {
	deleted []string
	forces  []bool
}

func (e *deleteEngine) Start(context.Context, ledger.StartRequest) (ledger.StartResult, error) {
	return ledger.StartResult{}, nil
}
func (e *deleteEngine) Cancel(context.Context, string) (ledger.CancelResult, error) {
	return ledger.CancelResult{}, nil
}
func (e *deleteEngine) Deliver(context.Context, string, bool) (ledger.DeliverResult, error) {
	return ledger.DeliverResult{}, nil
}
func (e *deleteEngine) Delete(_ context.Context, runID string, force bool) (ledger.DeleteResult, error) {
	e.deleted = append(e.deleted, runID)
	e.forces = append(e.forces, force)
	return ledger.DeleteResult{RunID: runID, Status: "delivery_pending", Deleted: true}, nil
}

// TestDeleteToolExecutes asserts the workflow_delete tool decodes run_id,
// forwards it to the engine, and encodes the DeleteResult within budget.
func TestDeleteToolExecutes(t *testing.T) {
	engine := &deleteEngine{}
	svc := testService(t, ledger.NewMemoryRepository(), engine)
	tool := findTool(t, svc, ledger.ToolWorkflowDelete)

	if tool.Name() != ledger.ToolWorkflowDelete {
		t.Fatalf("Name = %q, want %q", tool.Name(), ledger.ToolWorkflowDelete)
	}
	if tool.Class() != "write" {
		t.Fatalf("Class = %q, want write", tool.Class())
	}
	if tool.ResultBudgetBytes() <= 0 {
		t.Fatalf("ResultBudgetBytes = %d, want > 0", tool.ResultBudgetBytes())
	}
	if strings.TrimSpace(tool.Description()) == "" {
		t.Fatal("empty description")
	}
	params := tool.Parameters()
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["run_id"]; !ok {
		t.Fatal("parameters missing run_id")
	}
	req, _ := params["required"].([]string)
	if len(req) != 1 || req[0] != "run_id" {
		t.Fatalf("required = %v, want [run_id]", req)
	}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"run_id":"wfr-x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(engine.deleted) != 1 || engine.deleted[0] != "wfr-x" {
		t.Fatalf("engine deletes = %v, want [wfr-x]", engine.deleted)
	}
	if len(engine.forces) != 1 || engine.forces[0] {
		t.Fatalf("engine force flags = %v, want [false] when force is omitted", engine.forces)
	}
	if !strings.Contains(out, `"deleted":true`) || !strings.Contains(out, `"run_id":"wfr-x"`) {
		t.Fatalf("output = %s, want deleted:true for wfr-x", out)
	}
}

// TestDeleteToolForwardsForce asserts the workflow_delete tool decodes
// force=true and forwards it to the engine (the crash-recovery override).
func TestDeleteToolForwardsForce(t *testing.T) {
	engine := &deleteEngine{}
	svc := testService(t, ledger.NewMemoryRepository(), engine)
	tool := findTool(t, svc, ledger.ToolWorkflowDelete)

	params := tool.Parameters()
	props, _ := params["properties"].(map[string]any)
	if _, ok := props["force"]; !ok {
		t.Fatal("parameters missing force")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"run_id":"wfr-x","force":true}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(engine.forces) != 1 || !engine.forces[0] {
		t.Fatalf("engine force flags = %v, want [true]", engine.forces)
	}
	if len(engine.deleted) != 1 || engine.deleted[0] != "wfr-x" {
		t.Fatalf("engine deletes = %v, want [wfr-x]", engine.deleted)
	}
}

// TestDeleteToolInvalidArguments pins fail-closed arg parsing: malformed JSON
// and a missing required field both refuse without touching the engine.
func TestDeleteToolInvalidArguments(t *testing.T) {
	engine := &deleteEngine{}
	svc := testService(t, ledger.NewMemoryRepository(), engine)
	tool := findTool(t, svc, ledger.ToolWorkflowDelete)

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing run_id accepted")
	}
	if len(engine.deleted) != 0 {
		t.Fatalf("engine deletes = %v, want none", engine.deleted)
	}
}

// TestDeleteServiceRequiresRunID pins that blank run_id is refused before the
// engine is consulted.
func TestDeleteServiceRequiresRunID(t *testing.T) {
	engine := &deleteEngine{}
	svc := testService(t, ledger.NewMemoryRepository(), engine)
	if _, err := svc.Delete(context.Background(), "", false); err == nil {
		t.Fatal("empty run_id accepted")
	}
	if _, err := svc.Delete(context.Background(), "   ", false); err == nil {
		t.Fatal("blank run_id accepted")
	}
	if len(engine.deleted) != 0 {
		t.Fatalf("engine deletes = %v, want none", engine.deleted)
	}
}

// TestDeleteServiceNoEngine pins the fail-closed refusal when no engine is
// configured (e.g. a read-only session).
func TestDeleteServiceNoEngine(t *testing.T) {
	svc, err := ledger.NewService(ledger.ServiceOptions{
		Repo: func(context.Context) (ledger.Repository, func(), error) {
			return ledger.NewMemoryRepository(), func() {}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := svc.Delete(context.Background(), "wfr-x", false); err == nil || !strings.Contains(err.Error(), "engine") {
		t.Fatalf("Delete without engine = %v, want engine refusal", err)
	}
}
