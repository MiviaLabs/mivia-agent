package chatsync

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// hookEvent builds the bus event the agent's emit path produces for one hook
// run, typed payload included - without it the projector cannot tell a hook
// that reported from one that refused a call.
func hookEvent(phase, program, tool, callID, output string, denied bool) events.Event {
	return events.Event{
		Kind:       events.KindHook,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: callID,
		Output:     output,
		Hook: &events.HookEvent{
			Phase: phase, Program: program, Tool: tool, Denied: denied,
		},
	}
}

func hookPayload(t *testing.T, got []WireEvent) *HookRanPayload {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("the hook produced %d wire events, want 1", len(got))
	}
	if got[0].Type != TypeHookRan {
		t.Fatalf("type = %s, want %s", got[0].Type, TypeHookRan)
	}
	payload, ok := got[0].Payload.(*HookRanPayload)
	if !ok {
		t.Fatalf("payload is %T, want *HookRanPayload", got[0].Payload)
	}
	return payload
}

// TestABlockedCallIsVisibleToARemoteReader is the case the event exists for. A
// blocked call emits no tool.ended, so a reader otherwise watches a
// tool.started that never finishes and is never told why.
func TestABlockedCallIsVisibleToARemoteReader(t *testing.T) {
	p := NewProjector("sess-1", 0, toolIOOpts())
	p.Project(events.Event{Kind: events.KindTurnStart, SessionID: "sess-1", TurnID: "turn:1"})

	payload := hookPayload(t, p.Project(
		hookEvent("PreToolUse", "guard.py", "run_command", "c1", "policy: no network", true)))

	if !payload.Blocked {
		t.Error("the refusal did not reach the wire; the reader is left watching a " +
			"tool call that never finishes, with no reason given")
	}
	if payload.ToolCallID != "c1" {
		t.Errorf("ToolCallID = %q; the row cannot be tied to the call it stopped",
			payload.ToolCallID)
	}
	if payload.Program != "guard.py" || payload.Phase != "PreToolUse" {
		t.Errorf("the hook's identity did not survive: %+v", payload)
	}
	if payload.Output != "policy: no network" {
		t.Errorf("Output = %q, want the reason the hook gave", payload.Output)
	}
}

// TestAHookWithNoTypedPayloadIsNotSent holds the projector to the reason the
// typed payload exists. Without it the row cannot say whether the call was
// refused, and a hook row that cannot answer that is worse than none.
func TestAHookWithNoTypedPayloadIsNotSent(t *testing.T) {
	p := NewProjector("sess-1", 0, toolIOOpts())
	p.Project(events.Event{Kind: events.KindTurnStart, SessionID: "sess-1", TurnID: "turn:1"})

	got := p.Project(events.Event{
		Kind: events.KindHook, SessionID: "sess-1", TurnID: "turn:1", Output: "something",
	})
	if len(got) != 0 {
		t.Errorf("a hook with no typed payload produced %d wire events; the row would "+
			"claim a verdict it does not have", len(got))
	}
}

// TestHookOutputRidesTheToolIOGate holds hook output to the same rule as tool
// output: it is text a local program printed, and it leaves the machine only
// when the operator opted in.
func TestHookOutputRidesTheToolIOGate(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts()) // IncludeToolIO is off
	p.Project(events.Event{Kind: events.KindTurnStart, SessionID: "sess-1", TurnID: "turn:1"})

	payload := hookPayload(t, p.Project(
		hookEvent("PreToolUse", "guard.py", "run_command", "c1", "policy: no network", true)))

	if payload.Output != "" {
		t.Errorf("Output = %q; hook output left the machine with tool IO disabled",
			payload.Output)
	}
	if payload.OutputBytes != len("policy: no network") {
		t.Errorf("OutputBytes = %d, want %d: a reader must be able to tell silence "+
			"from suppression", payload.OutputBytes, len("policy: no network"))
	}
	if !payload.Blocked {
		t.Error("withholding the text also withheld the verdict; the verdict is not " +
			"content and must always travel")
	}
}
