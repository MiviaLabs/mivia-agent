package subagents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// This file tests the PRODUCER half of the subagent event wiring.
//
// It exists because four features in a row shipped dead while every test
// stayed green, and each time the reason was the same shape: the consumer was
// tested against a value the test itself constructed, and nothing checked that
// anything real ever constructed it. `parent_task` rode a context value no
// path could populate; `subagent_begin` was dropped by a wrapper; the run's
// task text was read under a field name the producer had stopped sending.
//
// A test here must therefore drive the real construction path and assert on
// what comes out of it - never hand-build the value it is checking.

// TestPoolRequestCarriesTheParentTask proves the field survives the hop from
// Task to runtime.Request. Deleting it there made parent_task permanently
// empty again while the origin test - which builds its own Request - passed.
func TestPoolRequestCarriesTheParentTask(t *testing.T) {
	capture := &requestCapturingHandler{}
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)
	if err := d.Register(runtime.Subagent, "capture", capture); err != nil {
		t.Fatalf("Register: %v", err)
	}

	p := New(d, Policy{Workers: 1})
	if _, err := p.Run(context.Background(), []Task{{
		ID:           "child-1",
		Name:         "capture",
		ParentTaskID: "the-parent",
		Input:        json.RawMessage(`"work"`),
		Budget:       1,
	}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := capture.req; got.ParentTaskID != "the-parent" {
		t.Errorf("Request.ParentTaskID = %q, want the-parent - the task's parent "+
			"never reached the request, so no event can carry it", got.ParentTaskID)
	}
}

// requestCapturingHandler records the request the pool actually built, so a
// test asserts on the real construction rather than on one it wrote itself.
type requestCapturingHandler struct{ req runtime.Request }

func (h *requestCapturingHandler) Invoke(_ context.Context, req runtime.Request) (json.RawMessage, error) {
	h.req = req
	return json.RawMessage(`"ok"`), nil
}

// TestRunAnnouncesItselfBeforeItsFirstToolCall proves a REAL run emits the
// opening signal, and emits it before any tool event.
//
// It drives the handler end to end rather than calling announceRunStart
// directly. Calling the helper only proves the helper works: deleting its call
// site left every suite green, which is the precise shape that shipped this
// event dead once already.
func TestRunAnnouncesItselfBeforeItsFirstToolCall(t *testing.T) {
	srv := subagentHTTPServer(t, []struct {
		content   string
		toolCalls []provider.ToolCall
	}{{content: "done"}})
	defer srv.Close()

	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{
		Name: "test-sub", BaseURL: srv.URL, APIKey: "test-key",
	})
	d := runtime.New(runtime.Policy{})
	t.Cleanup(d.Close)

	var kinds []agent.EventKind
	var beginDetail string
	handler := &MultiStepHandler{
		Completer: comp,
		Model:     "sub-model",
		MaxSteps:  2,
		MaxTokens: 100,
		OnEvent: func(e agent.Event) {
			kinds = append(kinds, e.Kind)
			if e.Kind == agent.EventSubagentBegin {
				beginDetail = e.Detail
			}
		},
	}
	if err := d.Register(runtime.Subagent, "announcer", handler); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := handler.Invoke(context.Background(), runtime.Request{
		ID: "task-1", Name: "announcer", Input: json.RawMessage(`"review the diff"`),
	}); err != nil {
		t.Fatalf("Invoke: %v", err)
	}

	if len(kinds) == 0 || kinds[0] != agent.EventSubagentBegin {
		t.Fatalf("first event was %v, want %s to open the run", kinds, agent.EventSubagentBegin)
	}
	if beginDetail != "review the diff" {
		t.Errorf("Detail = %q, want the task text the projector turns into the "+
			"wire's task field", beginDetail)
	}
}

// TestAnnounceRunStartIsANoOpWithoutASink guards the nil case the production
// caller relies on: a handler with no OnEvent must not panic.
func TestAnnounceRunStartIsANoOpWithoutASink(t *testing.T) {
	announceRunStart(nil, "reviewer", "review the diff")
}

// TestSchemaRetryDiscardsTheRejectedReply proves the corrective re-entry emits
// the reset, not merely the notice.
//
// A retry that announces itself but does not discard what it replaces leaves
// the rejected attempt's prose in every viewer, with the replacement appended
// after it. The notice had a test; the discard did not.
func TestSchemaRetryDiscardsTheRejectedReply(t *testing.T) {
	// Two turns: the first returns text that fails schema validation, the
	// second returns valid JSON.
	srv := subagentHTTPServer(t, []struct {
		content   string
		toolCalls []provider.ToolCall
	}{
		{content: "not json at all"},
		{content: `{"answer":"ok"}`},
	})
	defer srv.Close()

	comp := provider.NewOpenAICompatWithOptions(provider.CompatOptions{
		Name: "test-sub", BaseURL: srv.URL, APIKey: "test-key",
	})

	var kinds []agent.EventKind
	handler := &MultiStepHandler{
		Completer:      comp,
		Model:          "sub-model",
		MaxSteps:       2,
		MaxTokens:      100,
		SchemaRetryMax: 1,
		OutputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"answer": map[string]any{"type": "string"}},
			"required":             []any{"answer"},
			"additionalProperties": false,
		},
		OnEvent: func(e agent.Event) { kinds = append(kinds, e.Kind) },
	}

	_, _ = handler.Invoke(context.Background(), runtime.Request{
		ID: "task-1", Name: "schema", Input: json.RawMessage(`"do the work"`),
	})

	var sawRetry, sawReset bool
	for i, k := range kinds {
		if k == agent.EventSchemaRetry {
			sawRetry = true
			// The discard must accompany the notice, not trail the whole run.
			for _, later := range kinds[i:] {
				if later == agent.EventAssistantReset {
					sawReset = true
				}
			}
		}
	}
	if !sawRetry {
		t.Fatalf("the schema retry never fired, so this test proved nothing. "+
			"events: %v", kinds)
	}
	if !sawReset {
		t.Errorf("a schema retry fired with no %s; the rejected reply stays on "+
			"screen with the replacement appended after it. events: %v",
			agent.EventAssistantReset, kinds)
	}
}
