package subagents_test

// A schema that cannot be admitted must fail the task before the model is
// called, and a cancelled task must stop before another attempt rather than
// spending the caller's budget after they gave up.

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

func remoteRefSchema() map[string]any {
	return map[string]any{"$ref": "https://example.com/schema.json"}
}

func TestMultiStepRefusesAnInadmissibleSchema(t *testing.T) {
	completer := &scriptedSchemaCompleter{replies: []string{`{"ok":true}`}}
	h := &subagents.MultiStepHandler{
		Completer: completer, FullRegistry: tools.NewRegistry(), Model: "m", MaxSteps: 3,
	}
	input, _ := json.Marshal("do the work")
	out, err := h.Invoke(context.Background(), runtime.Request{
		ID: "task-1", Name: "worker", Kind: runtime.Subagent, Input: input,
		OutputSchema: remoteRefSchema(),
	})
	if !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("err = %v, want ErrSchemaViolation", err)
	}
	if completer.i != 0 {
		t.Fatalf("the model was called %d times for an inadmissible schema", completer.i)
	}
	var payload map[string]any
	if len(out) > 0 {
		_ = json.Unmarshal(out, &payload)
	}
	if payload["status"] == "completed" {
		t.Fatalf("an inadmissible schema reported completion: %#v", payload)
	}
}

func TestMultiStepHandlerSchemaFallsBackToItsOwn(t *testing.T) {
	// No task schema: the handler's own schema is compiled and enforced.
	payload, err := invokeMultiStep(t, []string{`{"ok":true}`}, schemaObject(), 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if payload["schema"] != "ok" {
		t.Fatalf("payload = %#v", payload)
	}

	h := &subagents.MultiStepHandler{
		Completer:    &scriptedSchemaCompleter{replies: []string{`{"ok":true}`}},
		FullRegistry: tools.NewRegistry(), Model: "m", MaxSteps: 3,
		OutputSchema: remoteRefSchema(),
	}
	input, _ := json.Marshal("do the work")
	if _, err := h.Invoke(context.Background(), runtime.Request{
		ID: "task-2", Name: "worker", Kind: runtime.Subagent, Input: input,
	}); !errors.Is(err, subagents.ErrSchemaViolation) {
		t.Fatalf("handler schema admission = %v, want ErrSchemaViolation", err)
	}
}

func TestMultiStepStopsWhenTheCallerCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	completer := &scriptedSchemaCompleter{replies: []string{`{"ok":true}`}}
	h := &subagents.MultiStepHandler{
		Completer: completer, FullRegistry: tools.NewRegistry(), Model: "m", MaxSteps: 3,
		OutputSchema: schemaObject(),
	}
	input, _ := json.Marshal("do the work")
	out, err := h.Invoke(ctx, runtime.Request{
		ID: "task-3", Name: "worker", Kind: runtime.Subagent, Input: input,
	})
	if err == nil {
		t.Fatalf("a cancelled task reported success: %s", out)
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err = %v, want cancellation", err)
	}
}

// cancellingCompleter cancels the caller's context on its first reply and
// answers with output the schema rejects, so the retry loop is entered with a
// context that is already gone.
type cancellingCompleter struct {
	cancel context.CancelFunc
	calls  int
}

func (c *cancellingCompleter) Name() string { return "cancelling" }
func (c *cancellingCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", errors.New("Chat unused")
}
func (c *cancellingCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	return "", errors.New("ChatStream unused")
}
func (c *cancellingCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	c.calls++
	if c.calls == 1 {
		c.cancel()
	}
	return &provider.Response{Content: `{"not_ok":true}`, FinishReason: "stop"}, nil
}

func TestSchemaRetryStopsWhenTheContextDiedMidAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	completer := &cancellingCompleter{cancel: cancel}
	h := &subagents.MultiStepHandler{
		Completer: completer, FullRegistry: tools.NewRegistry(), Model: "m", MaxSteps: 6,
		OutputSchema: schemaObject(), SchemaRetryMax: 3,
	}
	input, _ := json.Marshal("do the work")
	out, err := h.Invoke(ctx, runtime.Request{
		ID: "task-4", Name: "worker", Kind: runtime.Subagent, Input: input,
	})
	if err == nil {
		t.Fatalf("a cancelled retry loop reported success: %s", out)
	}
	if completer.calls > 1 {
		t.Fatalf("the model was called %d times after cancellation", completer.calls)
	}
}
