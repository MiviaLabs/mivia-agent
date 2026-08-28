package subagents

// The done event must carry the run's true terminal status, not an
// unconditional "completed". terminalStatus classifies the run's exit error;
// the deferred emit in MultiStepHandler.run stamps that classification on
// EventSubagentDone. A panicking run recovers into a wrapped error result and
// stamps "error", so a panic can never read as "completed".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

func TestTerminalStatusClassifiesExitErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil means completed", nil, "completed"},
		{"bare canceled", context.Canceled, "canceled"},
		{"wrapped canceled", fmt.Errorf("turn aborted: %w", context.Canceled), "canceled"},
		{"bare deadline", context.DeadlineExceeded, "timed_out"},
		{"wrapped deadline", fmt.Errorf("request budget: %w", context.DeadlineExceeded), "timed_out"},
		{"schema violation", ErrSchemaViolation, "error"},
		{"wrapped schema violation", fmt.Errorf("validation: %w", ErrSchemaViolation), "error"},
		{"ordinary failure", errors.New("provider exploded"), "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalStatus(tc.err); got != tc.want {
				t.Fatalf("terminalStatus(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// recordingEvents is an OnEvent sink the status tests inspect afterwards.
type recordingEvents struct {
	events []agent.Event
}

func (r *recordingEvents) onEvent(e agent.Event) { r.events = append(r.events, e) }

func (r *recordingEvents) doneEvents() []agent.Event {
	var out []agent.Event
	for _, e := range r.events {
		if e.Kind == agent.EventSubagentDone {
			out = append(out, e)
		}
	}
	return out
}

// invokeWithRecording runs the handler against comp with a recording OnEvent.
func invokeWithRecording(t *testing.T, comp provider.Completer) (*recordingEvents, error) {
	t.Helper()
	rec := &recordingEvents{}
	h := &MultiStepHandler{
		Completer:    comp,
		FullRegistry: newTestRegistry(),
		Model:        "test-model",
		SystemPrompt: "Test sub-agent.",
		MaxSteps:     3,
		MaxTokens:    256,
		OnEvent:      rec.onEvent,
	}
	_, err := h.Invoke(t.Context(), runtime.Request{
		ID:    "task-status",
		Name:  "multi_step",
		Input: json.RawMessage(`"produce a report"`),
	})
	return rec, err
}

func TestMultiStepHandlerDoneEventCarriesTerminalStatus(t *testing.T) {
	cases := []struct {
		name       string
		comp       provider.Completer
		wantStatus string
		wantErr    bool
	}{
		{
			name:       "success_reports_completed",
			comp:       &multiStepMockCompleter{name: "test", responses: []string{"the report"}},
			wantStatus: "completed",
		},
		{
			name:       "canceled_failure_reports_canceled",
			comp:       &scriptedStepCompleter{name: "test", failErr: context.Canceled},
			wantStatus: "canceled",
			wantErr:    true,
		},
		{
			name:       "deadline_failure_reports_timed_out",
			comp:       &scriptedStepCompleter{name: "test", failErr: context.DeadlineExceeded},
			wantStatus: "timed_out",
			wantErr:    true,
		},
		{
			name:       "provider_failure_reports_error",
			comp:       &scriptedStepCompleter{name: "test", failErr: errors.New("provider exploded")},
			wantStatus: "error",
			wantErr:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, err := invokeWithRecording(t, tc.comp)
			if tc.wantErr && err == nil {
				t.Fatal("expected the injected run error to surface")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			done := rec.doneEvents()
			if len(done) != 1 {
				t.Fatalf("done emitted %d times, want exactly 1", len(done))
			}
			if got := done[0].Status; got != tc.wantStatus {
				t.Fatalf("done Status = %q, want %q (a run that failed must never report completed)", got, tc.wantStatus)
			}
		})
	}
}

// panickingCompleter simulates a provider client whose failure mode is a
// panic, not an error return.
type panickingCompleter struct{ name string }

func (p *panickingCompleter) Name() string { return p.name }

func (p *panickingCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	panic("simulated completer panic")
}

func (p *panickingCompleter) Chat(context.Context, provider.Request) (string, error) {
	panic("simulated completer panic")
}

func (p *panickingCompleter) ChatStream(context.Context, provider.Request, io.Writer) (string, error) {
	panic("simulated completer panic")
}

// TestMultiStepHandlerPanickingRunStampsErrorThenRepanics pins the panic
// contract of the deferred done-event emit: the event is stamped "error"
// BEFORE the panic continues, and the panic value still reaches the caller
// (in production, the pool's recover), so a panicked subagent can neither
// report "completed" nor vanish silently.
func TestMultiStepHandlerPanickingRunStampsError(t *testing.T) {
	rec := &recordingEvents{}
	h := &MultiStepHandler{
		Completer:    &panickingCompleter{name: "boom"},
		FullRegistry: newTestRegistry(),
		Model:        "test-model",
		SystemPrompt: "Test sub-agent.",
		MaxSteps:     3,
		MaxTokens:    256,
		OnEvent:      rec.onEvent,
	}

	out, err := h.Invoke(t.Context(), runtime.Request{
		ID:    "task-panic",
		Name:  "boom",
		Input: json.RawMessage(`"do work"`),
	})

	if err == nil {
		t.Fatal("a panicked run must return an error result, got nil")
	}
	if !strings.Contains(err.Error(), "simulated completer panic") {
		t.Fatalf("error = %v, want the panic value preserved in the message", err)
	}
	var envelope struct {
		Status string `json:"status"`
	}
	if uerr := json.Unmarshal(out, &envelope); uerr != nil {
		t.Fatalf("result body is not a JSON envelope: %v", uerr)
	}
	if envelope.Status != "error" {
		t.Fatalf("panicked run's result Status = %q, want %q", envelope.Status, "error")
	}
	done := rec.doneEvents()
	if len(done) != 1 {
		t.Fatalf("done emitted %d times, want exactly 1 (even a panic announces it)", len(done))
	}
	if got := done[0].Status; got != "error" {
		t.Fatalf("panicked run's done Status = %q, want %q", got, "error")
	}
}
