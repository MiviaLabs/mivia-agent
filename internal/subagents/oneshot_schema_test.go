package subagents

// dispatch_tasks tells the model an output_schema is "Validated before the
// task completes". The agent field is optional, and an agent-less task routes
// to OneShotHandler, which never read req.OutputSchema - so that promise was
// silently false for every agent-less task: unvalidated prose came back
// reporting "completed", with no schema key to tell a consumer that
// validation had been skipped.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// scriptedChatCompleter returns canned replies in order, recording every
// request so the test can assert on what reached the wire.
type scriptedChatCompleter struct {
	replies  []string
	calls    int
	requests []provider.Request
}

func (c *scriptedChatCompleter) Name() string { return "scripted" }

func (c *scriptedChatCompleter) Chat(_ context.Context, req provider.Request) (string, error) {
	c.requests = append(c.requests, req)
	reply := "{}"
	if c.calls < len(c.replies) {
		reply = c.replies[c.calls]
	}
	c.calls++
	return reply, nil
}

func (c *scriptedChatCompleter) ChatStream(_ context.Context, _ provider.Request, _ io.Writer) (string, error) {
	return "", nil
}

func (c *scriptedChatCompleter) ChatTurn(ctx context.Context, req provider.Request) (*provider.Response, error) {
	content, err := c.Chat(ctx, req)
	if err != nil {
		return nil, err
	}
	return &provider.Response{Content: content}, nil
}

func oneShotSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"verdict": map[string]any{"type": "string"}},
		"required":             []any{"verdict"},
		"additionalProperties": false,
	}
}

func oneShotRequest(schema map[string]any) runtime.Request {
	return runtime.Request{
		Name:         "oneshot",
		Input:        json.RawMessage(`"decide something"`),
		OutputSchema: schema,
	}
}

// A reply that satisfies the schema comes back as parsed structure, marked
// validated, so a consumer can tell it was checked.
func TestOneShotHandlerValidatesAgainstOutputSchema(t *testing.T) {
	completer := &scriptedChatCompleter{replies: []string{`{"verdict":"ship it"}`}}
	h := &OneShotHandler{Completer: completer, Model: "m", SystemPrompt: "be brief"}

	out, err := h.Invoke(context.Background(), oneShotRequest(oneShotSchema()))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["schema"] != "ok" {
		t.Errorf("envelope schema = %v, want \"ok\": a consumer cannot otherwise tell validation ran", envelope["schema"])
	}
	body, ok := envelope["output"].(map[string]any)
	if !ok {
		t.Fatalf("output = %T, want the parsed object", envelope["output"])
	}
	if body["verdict"] != "ship it" {
		t.Errorf("verdict = %v, want %q", body["verdict"], "ship it")
	}
}

// The model has to be TOLD the contract, or asking it to meet one is a guess.
func TestOneShotHandlerSendsTheSchemaToTheModel(t *testing.T) {
	completer := &scriptedChatCompleter{replies: []string{`{"verdict":"ok"}`}}
	h := &OneShotHandler{Completer: completer, Model: "m", SystemPrompt: "be brief"}

	if _, err := h.Invoke(context.Background(), oneShotRequest(oneShotSchema())); err != nil {
		t.Fatal(err)
	}
	if len(completer.requests) == 0 {
		t.Fatal("no request reached the completer")
	}
	var prompt string
	for _, msg := range completer.requests[0].Messages {
		prompt += msg.Content
	}
	if !strings.Contains(prompt, "verdict") {
		t.Error("the schema never reached the model; the task was asked to satisfy a contract it was not shown")
	}
}

// Prose where an object was required must not be reported as a completed task.
func TestOneShotHandlerRejectsUnvalidatableReply(t *testing.T) {
	completer := &scriptedChatCompleter{replies: []string{"I think we should ship it.", "still prose", "prose again"}}
	h := &OneShotHandler{Completer: completer, Model: "m", SystemPrompt: "be brief"}

	_, err := h.Invoke(context.Background(), oneShotRequest(oneShotSchema()))
	if err == nil {
		t.Fatal("a reply that does not satisfy the schema must not complete the task")
	}
	if !errors.Is(err, ErrSchemaViolation) {
		t.Errorf("err = %v, want ErrSchemaViolation", err)
	}
}

// A task with no schema keeps its plain-text envelope unchanged.
func TestOneShotHandlerWithoutSchemaIsUnchanged(t *testing.T) {
	completer := &scriptedChatCompleter{replies: []string{"a plain answer"}}
	h := &OneShotHandler{Completer: completer, Model: "m", SystemPrompt: "be brief"}

	out, err := h.Invoke(context.Background(), oneShotRequest(nil))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["output"] != "a plain answer" {
		t.Errorf("output = %v, want the plain reply", envelope["output"])
	}
	if _, present := envelope["schema"]; present {
		t.Error("a task with no schema must not claim a schema verdict")
	}
}

// The one-attempt contract is request-scoped and must reach the wire here too,
// exactly as it does on the multi-step path.
func TestOneShotHandlerForwardsDisableProviderReplay(t *testing.T) {
	completer := &scriptedChatCompleter{replies: []string{"fine"}}
	h := &OneShotHandler{Completer: completer, Model: "m", SystemPrompt: "be brief"}

	req := oneShotRequest(nil)
	req.DisableProviderReplay = true
	if _, err := h.Invoke(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !completer.requests[0].DisableProviderReplay {
		t.Error("DisableProviderReplay was dropped; a transient failure will replay a billable call")
	}
}
