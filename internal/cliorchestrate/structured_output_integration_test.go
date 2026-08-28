package cliorchestrate

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func TestAdmissionRejectsRemoteRefSchema(t *testing.T) {
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{Name: "worker", EffectiveTools: []string{"read_file"}})
	tool := &dispatchTasksTool{agentReg: reg, cfg: config.DefaultSubagentConfig}
	_, err := tool.buildTasks("", []dispatchTaskParam{{
		ID: "t1", Agent: "worker", Prompt: "work",
		OutputSchema: map[string]any{"$ref": "https://example.com/s.json"},
	}}, 30)
	if err == nil || !strings.Contains(err.Error(), "output_schema") {
		t.Fatalf("want admission reject, got %v", err)
	}
}

func TestAdmissionRejectsOversizedSchema(t *testing.T) {
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{Name: "worker"})
	tool := &dispatchTasksTool{agentReg: reg, cfg: config.DefaultSubagentConfig}
	_, err := tool.buildTasks("", []dispatchTaskParam{{
		ID: "t1", Agent: "worker", Prompt: "work",
		OutputSchema: map[string]any{
			"type":        "object",
			"description": strings.Repeat("z", jschema.MaxSchemaBytes+10),
		},
	}}, 30)
	if err == nil {
		t.Fatal("want oversized schema reject")
	}
}

func TestEncodeDispatchResultSchemaOK(t *testing.T) {
	// Multi-step success envelope with schema ok and structured output.
	body, _ := json.Marshal(map[string]any{
		"output": map[string]any{"ok": true},
		"schema": "ok", "status": "completed",
		"steps": 1, "elapsed": "1ms", "step_count": 1,
	})
	tr := EncodeOneDispatchResult(subagents.Result{
		TaskID: "t1", Status: "completed", Output: body,
	}, nil, 4096)
	if tr.Schema != "ok" {
		t.Fatalf("schema=%q", tr.Schema)
	}
	// Parent sees the multi_step envelope as JSON (modelVisibleOutput).
	raw, ok := tr.Output.(json.RawMessage)
	if !ok {
		t.Fatalf("output type %T", tr.Output)
	}
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	inner, ok := env["output"].(map[string]any)
	if !ok || inner["ok"] != true {
		t.Fatalf("inner output=%#v", env["output"])
	}
}

func TestEncodeDispatchResultSchemaViolationNoInlineBody(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"schema": "violation", "status": "error", "step_count": 2,
	})
	tr := EncodeOneDispatchResult(subagents.Result{
		TaskID: "t1", Status: "failed", Output: body,
		Err: errors.New("schema_violation: still bad"),
	}, nil, 4096)
	// terminationReason only type-asserts ErrSchemaViolation; plain errors.New
	// falls through to "failed". Use the real sentinel.
	tr = EncodeOneDispatchResult(subagents.Result{
		TaskID: "t1", Status: "failed", Output: body,
		Err: subagents.ErrSchemaViolation,
	}, nil, 4096)
	if tr.Reason != "schema_violation" {
		t.Fatalf("reason=%q", tr.Reason)
	}
	if tr.Schema != "violation" {
		t.Fatalf("schema=%q", tr.Schema)
	}
	if tr.Output != nil {
		t.Fatalf("must not inline body on schema_violation: %#v", tr.Output)
	}
}

func TestTerminationReasonSchemaViolation(t *testing.T) {
	got := terminationReason(subagents.Result{Err: subagents.ErrSchemaViolation})
	if got != "schema_violation" {
		t.Fatalf("got %q", got)
	}
}
