package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// A refused tool call must not be reported to the operator as one that ran.
//
// The two halves of a denial used to disagree. The model was told "tool call
// denied by user"; every viewer was told the call completed. A denial returns
// from the approval wrapper without entering the dispatcher shim, so nothing
// recorded an outcome for the call, and the loop's no-outcome fallback emitted
// a tool_end reading "completed (duplicate)". Both status mappings read that
// as success - the NDJSON writer's toolEndStatus returns "ok" for it, and the
// TUI computes OK = true - so an operator could refuse a command and watch the
// transcript say it ran.

// TestADeniedCallIsRecordedAsFailedNotCompleted drives the recorder the
// approval wrapper calls, and asserts on the detail the loop's emitter derives
// from it - the same string every surface classifies on.
func TestADeniedCallIsRecordedAsFailedNotCompleted(t *testing.T) {
	var turn sdkTurnState
	turn.recordToolOutcome("call-1", "run_command", "tool call denied by user: denied", true)

	outcome := turn.takeToolCallOutcome("call-1")
	if outcome == nil {
		t.Fatal("the denial recorded no outcome, so the loop falls back to " +
			"\"completed (duplicate)\" and the refusal is reported as a success")
	}

	detail := sdkToolEndDetail(*outcome)
	if !strings.HasPrefix(detail, "failed") {
		t.Errorf("detail = %q; every surface classifies on this prefix, so a "+
			"refused call without it renders as a call that ran and succeeded",
			detail)
	}
	if strings.Contains(detail, "duplicate") {
		t.Errorf("detail = %q; a refused call is not a dedup-cache hit", detail)
	}
	if !strings.Contains(outcome.body, "denied") {
		t.Errorf("body = %q, want the refusal the model was given", outcome.body)
	}
}

// TestTheFallbackStillCoversARealDuplicate holds the behaviour the denial fix
// must not disturb: a call the dedup cache served really did produce a result
// the model saw, and must keep reading as completed rather than failed.
func TestTheFallbackStillCoversARealDuplicate(t *testing.T) {
	var turn sdkTurnState
	turn.recordToolOutcomeWithPreview("call-2", "read_file", "the notice", false, "", true, "the original body")

	outcome := turn.takeToolCallOutcome("call-2")
	if outcome == nil {
		t.Fatal("no outcome recorded for the duplicate")
	}
	if detail := sdkToolEndDetail(*outcome); strings.HasPrefix(detail, "failed") {
		t.Errorf("detail = %q; a dedup-served call is not a failure", detail)
	}
}

// TestEmitCarriesTheHookVerdictAcrossTheBus proves the producer hop, not the
// consumer.
//
// The bus converts only agent.Event's generic string fields, so a hook's
// phase, program, tool and - above all - its DENIED flag stopped at that
// boundary. Every consumer past it (the chat-sync projector, the relay) could
// then see that a hook ran and not whether it refused the call, which is the
// only thing about a hook a reader must not be left to infer.
func TestEmitCarriesTheHookVerdictAcrossTheBus(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)

	got := make(chan events.Event, 1)
	bus.Subscribe(events.KindHook, events.HandlerFunc(func(_ context.Context, ev events.Event) { got <- ev }))

	emit(Options{EventBus: bus, SessionID: "sess-1", TurnID: "turn:1"}, Event{
		Kind:       EventHook,
		Name:       "PreToolUse",
		Program:    "guard.py",
		Tool:       "run_command",
		ToolCallID: "c1",
		// What a local surface is shown: stdout plus the operator diagnostic,
		// which names the hook's absolute path.
		Output:     "policy: no network\nhook /home/operator/.mivia/hooks/guard.py exited 2",
		HookStdout: "policy: no network",
		Denied:     true,
	})

	select {
	case ev := <-got:
		if ev.Hook == nil {
			t.Fatal("the hook crossed the bus with no typed payload, so no consumer " +
				"can tell a hook that reported from one that refused a tool call")
		}
		if !ev.Hook.Denied {
			t.Error("the refusal did not survive the bus")
		}
		if ev.Hook.Program != "guard.py" || ev.Hook.Phase != "PreToolUse" || ev.Hook.Tool != "run_command" {
			t.Errorf("the hook's identity did not survive: %+v", ev.Hook)
		}
		// The typed payload is what crosses to another machine, so it carries
		// the hook's stdout alone - never the diagnostic that names the
		// operator's filesystem.
		if ev.Hook.Output != "policy: no network" {
			t.Errorf("Hook.Output = %q, want the hook's stdout alone", ev.Hook.Output)
		}
		if strings.Contains(ev.Hook.Output, "/home/operator") {
			t.Error("the operator's filesystem path crossed the bus on the typed payload")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no hook event reached the bus")
	}
}

// TestEmitAddsNoHookPayloadToOtherKinds keeps the branch specific. A payload on
// every kind would be a lie about what those events describe.
func TestEmitAddsNoHookPayloadToOtherKinds(t *testing.T) {
	bus := events.New()
	t.Cleanup(bus.Close)

	got := make(chan events.Event, 1)
	bus.Subscribe(events.KindToolEnd, events.HandlerFunc(func(_ context.Context, ev events.Event) { got <- ev }))

	emit(Options{EventBus: bus, SessionID: "sess-1", TurnID: "turn:1"}, Event{
		Kind: EventToolEnd, Name: "run_command", ToolCallID: "c1", Denied: true,
	})

	select {
	case ev := <-got:
		if ev.Hook != nil {
			t.Errorf("a tool_end carried a hook payload: %+v", ev.Hook)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no tool end event reached the bus")
	}
}
