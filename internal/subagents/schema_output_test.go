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

// invokeMultiStepCounted is invokeMultiStep plus the number of LLM turns the
// completer actually served, so tests can assert on LLM-call budgets.
func invokeMultiStepCounted(t *testing.T, replies []string, schema map[string]any, maxSteps, retryMax int) (map[string]any, int, error) {
	t.Helper()
	c := &scriptedSchemaCompleter{replies: replies}
	reg := tools.NewRegistry()
	h := &subagents.MultiStepHandler{
		Completer:      c,
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
	return payload, c.i, err
}

func TestMultiStepSchemaNoProgressFailsFast(t *testing.T) {
	// A model that repeats the byte-identical invalid candidate after the
	// corrective turn cannot be repaired by more retries: it must fail after
	// exactly 2 LLM calls (initial + one confirm), not burn the whole
	// retryMax+1 budget on the same dead end.
	replies := []string{
		`{"ok":"yes"}`,
		`{"ok":"yes"}`,
		`{"ok":"yes"}`,
		`{"ok":"yes"}`,
	}
	payload, calls, err := invokeMultiStepCounted(t, replies, schemaObject(), 10, 2)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err=%v, want ErrSchemaViolation", err)
	}
	if !strings.Contains(err.Error(), "no progress on schema repair") {
		t.Fatalf("err=%v, want no-progress cause", err)
	}
	if calls != 2 {
		t.Fatalf("got %d LLM calls, want exactly 2 (initial + one confirm)", calls)
	}
	if payload["schema"] != "violation" {
		t.Fatalf("schema=%v payload=%#v", payload["schema"], payload)
	}
	if _, has := payload["output"]; has {
		t.Fatalf("malformed output must not be inlined: %#v", payload)
	}
}

func TestMultiStepSchemaDistinctInvalidGetsFullBudget(t *testing.T) {
	// Distinct invalid candidates each earn a corrective re-entry; the full
	// retryMax+1 budget is consumed before failing.
	replies := []string{`nope`, `still nope`, `and again`, `one more`}
	payload, calls, err := invokeMultiStepCounted(t, replies, schemaObject(), 10, 2)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err=%v, want ErrSchemaViolation", err)
	}
	if calls != 3 {
		t.Fatalf("got %d LLM calls, want retryMax+1 = 3 for distinct invalid outputs", calls)
	}
	if payload["schema"] != "violation" {
		t.Fatalf("schema=%v payload=%#v", payload["schema"], payload)
	}
}

// repeatingCompleter always returns the same reply, regardless of how many
// times it is called, and counts every call - including ones a scripted
// completer's fallback branch would leave uncounted. Used to prove an
// extraction step succeeds on the FIRST attempt rather than being masked by
// a later retry.
type repeatingCompleter struct {
	reply string
	calls int
}

func (c *repeatingCompleter) Name() string { return "repeating" }
func (c *repeatingCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", errors.New("Chat unused")
}
func (c *repeatingCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", errors.New("ChatStream unused")
}
func (c *repeatingCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	c.calls++
	return &provider.Response{Content: c.reply, FinishReason: "stop"}, nil
}

// TestMultiStepSchemaEnvelopeWrappedReplyValidates pins the new extraction
// step: a reply that wraps its JSON in <mivia_output> tags, with narration
// before and after, must validate on the FIRST attempt. The completer always
// returns the same narration-wrapped reply, so without envelope extraction
// every retry attempt fails identically and the call exhausts its retry
// budget with ErrSchemaViolation; with extraction, the very first attempt
// succeeds and no retry ever happens.
func TestMultiStepSchemaEnvelopeWrappedReplyValidates(t *testing.T) {
	reply := "Sure, here is the result:\n<mivia_output>\n{\"ok\":true}\n</mivia_output>\nLet me know if you need anything else."
	c := &repeatingCompleter{reply: reply}
	reg := tools.NewRegistry()
	h := &subagents.MultiStepHandler{
		Completer:      c,
		FullRegistry:   reg,
		Model:          "m",
		MaxSteps:       3,
		SchemaRetryMax: 2,
		OutputSchema:   schemaObject(),
	}
	input, _ := json.Marshal("do the work")
	out, err := h.Invoke(context.Background(), runtime.Request{
		ID: "task-1", Name: "worker", Kind: runtime.Subagent, Input: input,
		OutputSchema: schemaObject(),
	})
	if err != nil {
		t.Fatalf("envelope-wrapped reply must validate on the first attempt: %v", err)
	}
	var payload map[string]any
	if len(out) > 0 {
		_ = json.Unmarshal(out, &payload)
	}
	if payload["schema"] != "ok" {
		t.Fatalf("envelope-wrapped reply should validate: %#v", payload)
	}
	if c.calls != 1 {
		t.Fatalf("got %d LLM calls, want exactly 1 (validated on first attempt via envelope extraction, no retry)", c.calls)
	}
}

// TestMultiStepSchemaFailFastOnIdenticalEnvelopeDespiteDifferentPreamble pins
// the accepted Step 0 behavior change: raw replies whose envelope content is
// byte-identical but whose surrounding narration differs now collapse to the
// same extracted candidate, so the no-progress fail-fast fires on the first
// confirm instead of burning the full retry budget. Four replies (mirroring
// TestMultiStepSchemaNoProgressFailsFast) keep the scripted completer's
// clean-JSON fallback response out of reach.
func TestMultiStepSchemaFailFastOnIdenticalEnvelopeDespiteDifferentPreamble(t *testing.T) {
	replies := []string{
		"Sure!\n<mivia_output>\n{\"ok\":\"yes\"}\n</mivia_output>",
		"Here you go:\n<mivia_output>\n{\"ok\":\"yes\"}\n</mivia_output>",
		"Certainly.\n<mivia_output>\n{\"ok\":\"yes\"}\n</mivia_output>",
		"Done!\n<mivia_output>\n{\"ok\":\"yes\"}\n</mivia_output>",
	}
	payload, calls, err := invokeMultiStepCounted(t, replies, schemaObject(), 10, 2)
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err=%v, want ErrSchemaViolation", err)
	}
	if !strings.Contains(err.Error(), "no progress on schema repair") {
		t.Fatalf("err=%v, want no-progress cause", err)
	}
	if calls != 2 {
		t.Fatalf("got %d LLM calls, want exactly 2 (identical envelope content despite different preamble)", calls)
	}
	if payload["schema"] != "violation" {
		t.Fatalf("schema=%v payload=%#v", payload["schema"], payload)
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
