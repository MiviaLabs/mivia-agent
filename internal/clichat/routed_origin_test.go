package clichat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// TestStampRoutedOrigin_PreservesCoordinatorTaskID is the regression for the
// live subagent-thread dialog showing nothing.
//
// A dispatch_tasks task routed to a named agent has its events stamped by
// subagents.MultiStepHandler with the COORDINATOR's canonical task id (the
// model-authored "id" from the dispatch_tasks call, e.g. "core-architecture"),
// taken from runtime.TaskIdentityFrom(ctx). That id is the correlation key
// every consumer looks work up by: uiadapter.SubagentThreads registers a live
// thread under it, the TUI sidebar row is keyed by it, and the workflow
// liveness watchdog (controller.NoteStepHeartbeat) counts against it.
//
// The routed-agent handler used to OVERWRITE it with a freshly minted opaque
// runtime.NewSessionID(), so the live thread was filed under a random key the
// UI could never look up - opening the dialog always missed and fell back to
// an empty step log. The invocation identity still travels on e.Identity;
// only the correlation id must survive.
func TestStampRoutedOrigin_PreservesCoordinatorTaskID(t *testing.T) {
	var got agent.Event
	fn := stampRoutedOrigin(nil, "instance-opaque-xyz", func(e agent.Event) { got = e })

	fn(agent.Event{
		Kind:   agent.EventToolStart,
		Name:   "read_file",
		Origin: agent.EventOrigin{TaskID: "core-architecture", Agent: "reviewer", Depth: 1},
	})

	if got.Origin.TaskID != "core-architecture" {
		t.Fatalf("Origin.TaskID = %q, want the coordinator's task id preserved (a clobbered id is unlookupable by the UI)", got.Origin.TaskID)
	}
	if got.Origin.Agent != "reviewer" || got.Origin.Depth != 1 {
		t.Errorf("rest of origin mutated: %+v", got.Origin)
	}
}

// A non-coordinator invocation carries no correlation id, so the opaque
// instance id is the only attribution available and must still be applied.
func TestStampRoutedOrigin_FallsBackToInstanceIDWhenUnset(t *testing.T) {
	var got agent.Event
	fn := stampRoutedOrigin(nil, "instance-opaque-xyz", func(e agent.Event) { got = e })

	fn(agent.Event{Kind: agent.EventToolStart, Name: "read_file"})

	if got.Origin.TaskID != "instance-opaque-xyz" {
		t.Fatalf("Origin.TaskID = %q, want the instance id as the no-correlation fallback", got.Origin.TaskID)
	}
}
