package clichat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// forwardedKinds is the table of what OnEventForMultiStep does with each kind
// a subagent loop can produce. It is the wrapper EVERY production wiring
// installs (dispatcher_handlers plain and skill paths, agent_task_handler's
// routed path), and it has no default arm, so an unnamed kind is discarded in
// silence.
//
// This table exists because a producer, the bus allowlist, the sync kinds, the
// relay kinds, the projector and the recorded contract were all wired for
// EventSubagentBegin while this wrapper still dropped it. Every one of those
// layers had a test; none of them covered the path, so a whole wire type
// shipped that could never fire.
var forwardedKinds = []struct {
	name     string
	in       agent.EventKind
	wantKind agent.EventKind
	wantDrop bool
	// wantOriginKept asserts attribution. EventToolPending is the one kind
	// whose destination is the ROOT approval surface - its forward strips
	// the origin (see the arm in OnEventForMultiStep), because the root
	// turn stream diverts origin-stamped events to the subagent dialog,
	// which has no prompt to arm. Every other kind must keep its origin.
	wantOriginKept bool
}{
	// Nested tool calls are REMAPPED so a parent never sees a subagent's raw
	// tool events.
	{"tool start remaps", agent.EventToolStart, agent.EventSubagentStart, false, true},
	{"tool end remaps", agent.EventToolEnd, agent.EventSubagentEnd, false, true},
	// Run lifecycle and progress pass through unchanged.
	{"begin forwards", agent.EventSubagentBegin, agent.EventSubagentBegin, false, true},
	{"done forwards", agent.EventSubagentDone, agent.EventSubagentDone, false, true},
	{"heartbeat forwards", agent.EventSubagentHeartbeat, agent.EventSubagentHeartbeat, false, true},
	{"step becomes heartbeat", agent.EventStep, agent.EventSubagentHeartbeat, false, true},
	{"heartbeat tick becomes subagent heartbeat", agent.EventHeartbeat, agent.EventSubagentHeartbeat, false, true},
	// Prose passes through so a remote viewer can open a subagent's thread.
	{"assistant forwards", agent.EventAssistant, agent.EventAssistant, false, true},
	// The discard must travel with the prose it discards. This row exists
	// because the reset was added to the producer, the projector, the wire
	// contract and the owned doc, and was dropped HERE - the same omission,
	// in the same switch, that had already swallowed EventSubagentBegin.
	{"assistant reset forwards", agent.EventAssistantReset, agent.EventAssistantReset, false, true},
	{"thinking forwards", agent.EventThinking, agent.EventThinking, false, true},
	// Forwarded with the origin STRIPPED: the gate the prompt belongs to can
	// only be answered from the root approval queue, and an origin-stamped
	// pending never got there - the gate blocked to the task deadline with
	// nothing on screen.
	{"tool pending forwards to the root surface", agent.EventToolPending, agent.EventToolPending, false, false},
	// A kind with no arm is dropped. This row is what proves the switch is
	// still a deliberate allowlist rather than a pass-through.
	{"unmapped kind is dropped", agent.EventCacheUsage, "", true, true},
}

func TestOnEventForMultiStepForwardsEveryWiredKind(t *testing.T) {
	for _, tc := range forwardedKinds {
		t.Run(tc.name, func(t *testing.T) {
			var got []agent.Event
			forward := OnEventForMultiStep(func(e agent.Event) { got = append(got, e) })

			forward(agent.Event{
				Kind:   tc.in,
				Name:   "worker",
				Detail: "review the diff",
				Origin: agent.EventOrigin{TaskID: "t1", SessionID: "s1", TurnID: "turn:1"},
			})

			if tc.wantDrop {
				if len(got) != 0 {
					t.Fatalf("kind %s produced %d events, want 0 - the switch is an allowlist",
						tc.in, len(got))
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("kind %s produced %d events, want exactly 1", tc.in, len(got))
			}
			if got[0].Kind != tc.wantKind {
				t.Errorf("kind %s forwarded as %s, want %s", tc.in, got[0].Kind, tc.wantKind)
			}
			if tc.wantOriginKept {
				if got[0].Origin.TaskID != "t1" {
					t.Errorf("kind %s lost its origin; attribution must survive the wrapper", tc.in)
				}
			} else if !got[0].Origin.IsZero() {
				t.Errorf("kind %s kept its origin; the root turn stream diverts "+
					"origin-stamped events to the subagent dialog, which cannot arm a prompt", tc.in)
			}
		})
	}
}

// TestOnEventForMultiStepBeginCarriesTaskText proves the run-level opening
// signal keeps the task description the projector turns into the wire's
// prompt field. Forwarding the event but dropping its Detail would ship a
// started event with no task text, which is most of its value.
func TestOnEventForMultiStepBeginCarriesTaskText(t *testing.T) {
	var got []agent.Event
	forward := OnEventForMultiStep(func(e agent.Event) { got = append(got, e) })

	forward(agent.Event{
		Kind:   agent.EventSubagentBegin,
		Name:   "reviewer",
		Detail: "review the diff",
		Origin: agent.EventOrigin{TaskID: "t1", SessionID: "s1"},
	})

	if len(got) != 1 {
		t.Fatalf("begin produced %d events, want 1", len(got))
	}
	if got[0].Detail != "review the diff" {
		t.Errorf("Detail = %q, want the task text", got[0].Detail)
	}
	if got[0].Name != "reviewer" {
		t.Errorf("Name = %q, want reviewer", got[0].Name)
	}
}

// TestASubagentsApprovalPromptReachesTheRootSurface pins the answer path for
// a delegated write tool. The nested loop copies the root session's live
// approval gate, so a "once"/write-only policy makes the nested loop call it -
// and the gate blocks on a decision that can only arrive through the ROOT
// approval queue, armed by a tool_pending event on the root turn stream and
// resolved by ToolCallID. The subagent dialog has no prompt surface, so an
// origin-stamped pending was diverted there and dropped: the gate blocked to
// the task deadline (12h by default for dispatch_tasks) and denied "canceled"
// with nothing ever on screen. The forward must strip the origin, and the id
// the UI will resolve with must survive intact.
func TestASubagentsApprovalPromptReachesTheRootSurface(t *testing.T) {
	var got []agent.Event
	forward := OnEventForMultiStep(func(e agent.Event) { got = append(got, e) })

	forward(agent.Event{
		Kind:       agent.EventToolPending,
		ToolCallID: "call-9",
		Name:       "run_command",
		Detail:     "needs approval",
		Input:      `{"command":"deploy.sh"}`,
		InputBody:  `{"command":"deploy.sh"}`,
		Origin:     agent.EventOrigin{TaskID: "t1", SessionID: "s1", TurnID: "turn:1"},
	})

	if len(got) != 1 {
		t.Fatalf("tool pending produced %d events, want 1", len(got))
	}
	if !got[0].Origin.IsZero() {
		t.Fatal("the prompt kept its subagent origin; the root stream would divert " +
			"it to the subagent dialog and the gate would wait out the task deadline " +
			"with nothing on screen")
	}
	if got[0].ToolCallID != "call-9" {
		t.Fatal("the prompt lost its call id; Approver.Resolve matches decisions by " +
			"that id, so every operator answer would become a silent no-op")
	}
	if got[0].Name != "run_command" || got[0].Detail != "needs approval" || got[0].Input == "" {
		t.Errorf("prompt body incomplete: %+v - the operator approves what they can see", got[0])
	}
}

// TestStampRoutedOriginLeavesAnAddressedEventAlone pins the routed-path half
// of the same contract. The routed-agent wrapper re-stamps every event with
// the invocation's identity; the one zero-origin event it receives - the
// approval prompt the multi-step wrapper stripped on purpose - must pass
// through untouched, or the prompt is routed back into the dialog that cannot
// answer it.
func TestStampRoutedOriginLeavesAnAddressedEventAlone(t *testing.T) {
	var got []agent.Event
	sink := stampRoutedOrigin(&events.Identity{}, "instance-1", func(e agent.Event) { got = append(got, e) })

	sink(agent.Event{Kind: agent.EventToolPending, ToolCallID: "call-9", Name: "run_command"})
	if len(got) != 1 || !got[0].Origin.IsZero() {
		t.Fatalf("an addressed event was re-stamped: %+v - the approval prompt must "+
			"stay on the root turn stream", got)
	}

	// Attribution for everything else is unchanged.
	sink(agent.Event{Kind: agent.EventSubagentStart, Name: "worker", Origin: agent.EventOrigin{TaskID: "t1"}})
	if len(got) != 2 {
		t.Fatalf("stamped event produced %d events, want 1 more", len(got)-1)
	}
	if got[1].Origin.TaskID != "t1" {
		t.Errorf("TaskID = %q, want the coordinator's canonical id kept", got[1].Origin.TaskID)
	}
	if got[1].Identity == nil {
		t.Error("Identity was not applied to an attributed event")
	}
}
