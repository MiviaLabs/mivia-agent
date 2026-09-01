package clichat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
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
}{
	// Nested tool calls are REMAPPED so a parent never sees a subagent's raw
	// tool events.
	{"tool start remaps", agent.EventToolStart, agent.EventSubagentStart, false},
	{"tool end remaps", agent.EventToolEnd, agent.EventSubagentEnd, false},
	// Run lifecycle and progress pass through unchanged.
	{"begin forwards", agent.EventSubagentBegin, agent.EventSubagentBegin, false},
	{"done forwards", agent.EventSubagentDone, agent.EventSubagentDone, false},
	{"heartbeat forwards", agent.EventSubagentHeartbeat, agent.EventSubagentHeartbeat, false},
	{"step becomes heartbeat", agent.EventStep, agent.EventSubagentHeartbeat, false},
	{"heartbeat tick becomes subagent heartbeat", agent.EventHeartbeat, agent.EventSubagentHeartbeat, false},
	// Prose passes through so a remote viewer can open a subagent's thread.
	{"assistant forwards", agent.EventAssistant, agent.EventAssistant, false},
	{"thinking forwards", agent.EventThinking, agent.EventThinking, false},
	// A kind with no arm is dropped. This row is what proves the switch is
	// still a deliberate allowlist rather than a pass-through.
	{"unmapped kind is dropped", agent.EventCacheUsage, "", true},
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
			if got[0].Origin.TaskID != "t1" {
				t.Errorf("kind %s lost its origin; attribution must survive the wrapper", tc.in)
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
