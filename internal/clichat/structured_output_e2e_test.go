package clichat

import (
	"context"
	"encoding/json"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// sequenceCompleter returns successive ChatTurn Content replies (no tool calls).
// Used to drive multi-step schema validation through the real dispatch path.
type sequenceCompleter struct {
	name    string
	replies []string
	i       atomic.Int32
}

func (c *sequenceCompleter) Name() string { return c.name }
func (c *sequenceCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	r, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return r.Content, nil
}
func (c *sequenceCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	return c.Chat(ctx, req)
}
func (c *sequenceCompleter) ChatTurn(ctx context.Context, _ provider.Request) (*provider.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i := int(c.i.Add(1) - 1)
	if i >= len(c.replies) {
		return &provider.Response{Content: `{"ok":true}`, FinishReason: "stop"}, nil
	}
	return &provider.Response{Content: c.replies[i], FinishReason: "stop"}, nil
}

func okSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok": map[string]any{"type": "boolean"},
		},
		"required":             []any{"ok"},
		"additionalProperties": false,
	}
}

// newSchemaDispatchTool builds a real session dispatcher (agent → multi_step)
// and returns the model-facing dispatch_tasks tool registered on it.
func newSchemaDispatchTool(t *testing.T, replies []string, cfg config.SubagentConfig) (*cliorchestrate.DispatchTasksToolForTest, *runtime.Dispatcher) {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	repo := ledger.NewMemoryLedgerRepository()
	comp := &sequenceCompleter{name: "schema-e2e", replies: replies}
	if cfg.SchemaRetryMax <= 0 {
		cfg.SchemaRetryMax = 2
	}
	if cfg.NestedSteps == 0 {
		cfg.NestedSteps = 8
	}
	if cfg.InlineOutputBytes == 0 {
		cfg.InlineOutputBytes = 4096
	}
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:      reg,
		Completer:     comp,
		ProviderName:  "test",
		Model:         "test-model",
		Config:        cfg,
		Repo:          repo,
		AgentRegistry: testAgentRegistry(t, "worker"),
	})
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	raw, ok := reg.Get("dispatch_tasks")
	if !ok {
		d.Close()
		t.Fatal("dispatch_tasks not registered")
	}
	tool, ok := raw.(*cliorchestrate.DispatchTasksToolForTest)
	if !ok {
		d.Close()
		t.Fatalf("dispatch_tasks type %T", raw)
	}
	return tool, d
}

func dispatchWithSchema(t *testing.T, tool *cliorchestrate.DispatchTasksToolForTest, schema map[string]any) []map[string]any {
	t.Helper()
	args := map[string]any{
		"tasks": []map[string]any{{
			"id":            "t1",
			"agent":         "worker",
			"prompt":        "return structured result",
			"output_schema": schema,
		}},
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	body, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute transport err: %v body=%s", err, body)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("result not JSON array: %v body=%s", err, body)
	}
	if len(parsed) != 1 {
		t.Fatalf("want 1 task result, got %d body=%s", len(parsed), body)
	}
	return parsed
}

// TestE2E_DispatchSchemaValidFirstTry: full path
// dispatch_tasks → coordinator → agent MultiStepHandler → encodeResults.
func TestE2E_DispatchSchemaValidFirstTry(t *testing.T) {
	tool, closer := newSchemaDispatchTool(t, []string{`{"ok":true}`}, config.SubagentConfig{})
	defer closer.Close()

	parsed := dispatchWithSchema(t, tool, okSchema())
	if parsed[0]["status"] != "completed" {
		t.Fatalf("status=%v body=%v", parsed[0]["status"], parsed[0])
	}
	if parsed[0]["schema"] != "ok" {
		t.Fatalf("schema=%v want ok", parsed[0]["schema"])
	}
	// Parent envelope carries multi_step JSON; inner output is structured.
	out := unwrapMultiStepOutput(t, parsed[0]["output"])
	if out["ok"] != true {
		t.Fatalf("structured output=%#v", out)
	}
	if parsed[0]["reason"] != nil && parsed[0]["reason"] != "" {
		t.Fatalf("completed task should not set reason: %v", parsed[0]["reason"])
	}
}

// TestE2E_DispatchSchemaValidAfterRetry: invalid first model reply, then valid.
func TestE2E_DispatchSchemaValidAfterRetry(t *testing.T) {
	tool, closer := newSchemaDispatchTool(t, []string{`not-json`, `{"ok":true}`}, config.SubagentConfig{SchemaRetryMax: 2, NestedSteps: 8})
	defer closer.Close()

	parsed := dispatchWithSchema(t, tool, okSchema())
	if parsed[0]["status"] != "completed" || parsed[0]["schema"] != "ok" {
		t.Fatalf("want completed/ok after retry, got status=%v schema=%v full=%v",
			parsed[0]["status"], parsed[0]["schema"], parsed[0])
	}
}

// TestE2E_DispatchSchemaExhausted: retries fail → schema_violation, no inline body.
func TestE2E_DispatchSchemaExhausted(t *testing.T) {
	tool, closer := newSchemaDispatchTool(t, []string{`bad1`, `bad2`, `bad3`}, config.SubagentConfig{SchemaRetryMax: 2, NestedSteps: 10})
	defer closer.Close()

	parsed := dispatchWithSchema(t, tool, okSchema())
	if parsed[0]["status"] == "completed" {
		t.Fatalf("want non-completed, got %#v", parsed[0])
	}
	if parsed[0]["reason"] != "schema_violation" {
		t.Fatalf("reason=%v want schema_violation full=%v", parsed[0]["reason"], parsed[0])
	}
	if parsed[0]["schema"] != "violation" {
		t.Fatalf("schema=%v want violation", parsed[0]["schema"])
	}
	// Malformed body must not appear as inline output.
	if parsed[0]["output"] != nil {
		t.Fatalf("must not inline malformed output: %#v", parsed[0]["output"])
	}
}

// TestE2E_DispatchNoSchemaKeepsFreeText: without schema, free-text contract holds.
func TestE2E_DispatchNoSchemaKeepsFreeText(t *testing.T) {
	tool, closer := newSchemaDispatchTool(t, []string{`plain prose answer`}, config.SubagentConfig{})
	defer closer.Close()

	args, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{{
			"id": "t1", "agent": "worker", "prompt": "say something",
		}},
	})
	body, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed[0]["status"] != "completed" {
		t.Fatalf("status=%v", parsed[0]["status"])
	}
	if _, ok := parsed[0]["schema"]; ok {
		t.Fatalf("schema field must be omitted: %#v", parsed[0])
	}
	// Free-text lives inside multi_step envelope's output string.
	raw, ok := parsed[0]["output"]
	if !ok {
		t.Fatalf("missing output: %#v", parsed[0])
	}
	// modelVisibleOutput may return nested JSON object for multi_step payload.
	switch v := raw.(type) {
	case map[string]any:
		if s, _ := v["output"].(string); s != "plain prose answer" {
			t.Fatalf("inner output=%v", v["output"])
		}
		if _, has := v["schema"]; has {
			t.Fatalf("inner schema must be absent: %#v", v)
		}
	case string:
		if !strings.Contains(v, "plain prose answer") {
			t.Fatalf("output=%q", v)
		}
	default:
		// json.RawMessage path after marshal/unmarshal becomes map or string only
		t.Fatalf("unexpected output type %T", raw)
	}
}

// TestE2E_DispatchAdmissionRejectsRemoteRef: bad schema never reaches spawn.
func TestE2E_DispatchAdmissionRejectsRemoteRef(t *testing.T) {
	tool, closer := newSchemaDispatchTool(t, []string{`{"ok":true}`}, config.SubagentConfig{})
	defer closer.Close()

	args, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{{
			"id": "t1", "agent": "worker", "prompt": "x",
			"output_schema": map[string]any{"$ref": "https://evil.example/schema.json"},
		}},
	})
	body, err := tool.Execute(context.Background(), args)
	if err == nil {
		t.Fatalf("want admission error, got body=%s", body)
	}
	if !strings.Contains(err.Error(), "output_schema") && !strings.Contains(err.Error(), "admission") {
		t.Fatalf("err=%v", err)
	}
}

// TestE2E_DispatchSchemaStepBudgetBlocksRetry: NestedSteps=1 cannot re-enter.
func TestE2E_DispatchSchemaStepBudgetBlocksRetry(t *testing.T) {
	tool, closer := newSchemaDispatchTool(t, []string{`not-json`, `{"ok":true}`}, config.SubagentConfig{
		SchemaRetryMax: 2,
		NestedSteps:    1, // first attempt spends the only step
	})
	defer closer.Close()

	parsed := dispatchWithSchema(t, tool, okSchema())
	if parsed[0]["status"] == "completed" {
		t.Fatalf("want failure when step budget blocks retry: %#v", parsed[0])
	}
	if parsed[0]["reason"] != "schema_violation" && parsed[0]["status"] != "failed" {
		t.Fatalf("want schema/step failure, got %#v", parsed[0])
	}
}

func unwrapMultiStepOutput(t *testing.T, raw any) map[string]any {
	t.Helper()
	switch v := raw.(type) {
	case map[string]any:
		inner, ok := v["output"].(map[string]any)
		if !ok {
			t.Fatalf("want structured multi_step output, got %#v", v)
		}
		return inner
	default:
		// Re-encode and parse (json.RawMessage-like)
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		var env map[string]any
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("output not object: %v raw=%s", err, b)
		}
		inner, ok := env["output"].(map[string]any)
		if !ok {
			t.Fatalf("inner output not object: %#v", env)
		}
		return inner
	}
}
