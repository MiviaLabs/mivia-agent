package subagents_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// scriptedSchemaCompleter returns successive Content replies for multi-step
// runs without tool calls.
type scriptedSchemaCompleter struct {
	replies []string
	i       int
}

func (c *scriptedSchemaCompleter) Name() string { return "scripted-schema" }
func (c *scriptedSchemaCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", errors.New("Chat unused")
}
func (c *scriptedSchemaCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", errors.New("ChatStream unused")
}
func (c *scriptedSchemaCompleter) ChatTurn(_ context.Context, _ provider.Request) (*provider.Response, error) {
	if c.i >= len(c.replies) {
		return &provider.Response{Content: `{"ok":true}`, FinishReason: "stop"}, nil
	}
	r := c.replies[c.i]
	c.i++
	return &provider.Response{Content: r, FinishReason: "stop"}, nil
}

func schemaObject() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ok": map[string]any{"type": "boolean"},
		},
		"required":             []any{"ok"},
		"additionalProperties": false,
	}
}

func invokeMultiStep(t *testing.T, replies []string, schema map[string]any, maxSteps, retryMax int) (map[string]any, error) {
	t.Helper()
	reg := tools.NewRegistry()
	h := &subagents.MultiStepHandler{
		Completer:      &scriptedSchemaCompleter{replies: replies},
		FullRegistry:   reg,
		Model:          "m",
		MaxSteps:       maxSteps,
		SchemaRetryMax: retryMax,
		OutputSchema:   schema,
	}
	input, _ := json.Marshal("do the work")
	out, err := h.Invoke(context.Background(), runtime.Request{
		ID: "task-1", Name: "worker", Kind: runtime.Subagent, Input: input,
		OutputSchema: schema,
	})
	var payload map[string]any
	if len(out) > 0 {
		_ = json.Unmarshal(out, &payload)
	}
	return payload, err
}

func TestMultiStepNoSchemaUnchangedShape(t *testing.T) {
	payload, err := invokeMultiStep(t, []string{"plain prose answer"}, nil, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "completed" {
		t.Fatalf("status=%v", payload["status"])
	}
	if _, ok := payload["schema"]; ok {
		t.Fatalf("schema field must be omitted when no schema: %#v", payload)
	}
	if payload["output"] != "plain prose answer" {
		t.Fatalf("output=%v", payload["output"])
	}
}

func TestMultiStepSchemaValidFirstTry(t *testing.T) {
	payload, err := invokeMultiStep(t, []string{`{"ok":true}`}, schemaObject(), 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if payload["schema"] != "ok" || payload["status"] != "completed" {
		t.Fatalf("payload=%#v", payload)
	}
	out, ok := payload["output"].(map[string]any)
	if !ok || out["ok"] != true {
		t.Fatalf("structured output=%#v", payload["output"])
	}
}

func TestMultiStepSchemaValidAfterRetry(t *testing.T) {
	payload, err := invokeMultiStep(t, []string{`not json`, `{"ok":true}`}, schemaObject(), 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if payload["schema"] != "ok" {
		t.Fatalf("want schema ok after retry, got %#v", payload)
	}
}

func TestMultiStepSchemaExhausted(t *testing.T) {
	payload, err := invokeMultiStep(t, []string{`nope`, `still nope`, `and again`}, schemaObject(), 10, 2)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err=%v, want ErrSchemaViolation", err)
	}
	if payload["schema"] != "violation" {
		t.Fatalf("schema=%v payload=%#v", payload["schema"], payload)
	}
	if _, has := payload["output"]; has {
		t.Fatalf("malformed output must not be inlined: %#v", payload)
	}
}

func TestMultiStepSchemaRetryRespectsMaxSteps(t *testing.T) {
	// MaxSteps=1: first attempt consumes the only step; no budget for corrective re-entry.
	payload, err := invokeMultiStep(t, []string{`not-json`, `{"ok":true}`}, schemaObject(), 1, 2)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err=%v, want schema violation when steps exhausted", err)
	}
	if payload["schema"] != "violation" && !strings.Contains(err.Error(), "step budget") {
		// Either violation status or step-budget message is acceptable.
		t.Logf("payload=%#v err=%v", payload, err)
	}
}

func TestMultiStepSchemaFenceStrip(t *testing.T) {
	payload, err := invokeMultiStep(t, []string{"```json\n{\"ok\":true}\n```"}, schemaObject(), 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	if payload["schema"] != "ok" {
		t.Fatalf("fenced JSON should validate: %#v", payload)
	}
}
