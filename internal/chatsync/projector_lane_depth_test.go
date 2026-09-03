package chatsync

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// attributedAtDepth builds an event carrying a task id at an explicit depth.
// Depth 0 is the shape the root loop takes whenever anything upstream of the
// projector stamps an attribution key without a dispatch depth - the workflow
// progress sinks in internal/cliworkflow and internal/workflows/localengine
// publish exactly that.
func attributedAtDepth(kind events.Kind, task string, depth int, content, detail string) events.Event {
	ev := events.Event{
		Kind:      kind,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
		Content:   content,
		Detail:    detail,
	}
	return ev.WithAgentAttribution(task, "root", depth)
}

// TestProjectorLanesOnDepthNotTaskID pins the wire type of prose carrying a
// task id but NO dispatch depth to the ROOT types.
//
// This is the defect: the lane decision asked "does it have a task id?" while
// buildEnvelope stamps an agent origin only at depth > 0, so a depth-0
// attributed event shipped under `mivia.chat.v1.subagent.*` with no agent
// object in the payload. A consumer that splits the main transcript from the
// subagent lanes on the type prefix files it under a lane it cannot key, and
// the root agent's prose and reasoning vanish from the main history.
func TestProjectorLanesOnDepthNotTaskID(t *testing.T) {
	cases := []struct {
		name string
		ev   events.Event
		want string
	}{
		{"assistant delta at depth 0", attributedAtDepth(events.KindAssistant, "inst-1", 0, "hello", "delta"), TypeAssistantDelta},
		{"assistant aggregate at depth 0", attributedAtDepth(events.KindAssistant, "inst-1", 0, "hello", ""), TypeAssistantMessage},
		{"thinking at depth 0", attributedAtDepth(events.KindThinking, "inst-1", 0, "reasoning", ""), TypeThinkingDelta},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjector("sess-1", 0, proseOpts())
			out := p.Project(tc.ev)
			if len(out) != 1 {
				t.Fatalf("got %d wire events, want 1", len(out))
			}
			if out[0].Type != tc.want {
				t.Errorf("type = %q, want %q", out[0].Type, tc.want)
			}
		})
	}
}

// TestProjectorStillLanesDispatchedProse is the other half of the
// discriminator: switching to depth must not swallow a REAL subagent's output
// into the root transcript. originForRequest stamps req.Depth+1, so every
// dispatched run is at 1 or deeper.
func TestProjectorStillLanesDispatchedProse(t *testing.T) {
	cases := []struct {
		name string
		ev   events.Event
		want string
	}{
		{"assistant delta at depth 1", attributedAtDepth(events.KindAssistant, "task-a", 1, "hello", "delta"), TypeSubagentAssistantDelta},
		{"assistant aggregate at depth 1", attributedAtDepth(events.KindAssistant, "task-a", 1, "hello", ""), TypeSubagentAssistantMessage},
		{"thinking at depth 2", attributedAtDepth(events.KindThinking, "task-a", 2, "reasoning", ""), TypeSubagentThinkingDelta},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjector("sess-1", 0, proseOpts())
			out := p.Project(tc.ev)
			if len(out) != 1 {
				t.Fatalf("got %d wire events, want 1", len(out))
			}
			if out[0].Type != tc.want {
				t.Errorf("type = %q, want %q", out[0].Type, tc.want)
			}
		})
	}
}

// TestProjectorTypeAndEnvelopeAgreeOnLane is the invariant the two halves
// above exist to protect: an event's wire TYPE and its payload's agent origin
// must never disagree. A subagent-typed event with no agent object cannot be
// keyed to a lane by any consumer.
func TestProjectorTypeAndEnvelopeAgreeOnLane(t *testing.T) {
	kinds := []events.Kind{events.KindAssistant, events.KindThinking}
	for _, depth := range []int{0, 1, 2} {
		for _, kind := range kinds {
			p := NewProjector("sess-1", 0, proseOpts())
			out := p.Project(attributedAtDepth(kind, "task-x", depth, "text", ""))
			if len(out) != 1 {
				t.Fatalf("depth %d kind %v: got %d wire events", depth, kind, len(out))
			}
			subagentType := strings.HasPrefix(out[0].Type, subagentTypePrefix)
			raw, err := json.Marshal(out[0].Payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			var decoded struct {
				Agent *struct {
					Task  string `json:"task"`
					Depth int    `json:"depth"`
				} `json:"agent"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			hasOrigin := decoded.Agent != nil
			if subagentType != hasOrigin {
				t.Errorf("depth %d kind %v: type %q subagent=%v but agent origin present=%v",
					depth, kind, out[0].Type, subagentType, hasOrigin)
			}
		}
	}
}

const subagentTypePrefix = "mivia.chat.v1.subagent."
