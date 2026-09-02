package clichat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/hub"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

// This file is a CONFORMANCE suite over the viewer surfaces, not another
// per-surface test.
//
// Every per-surface test answers "does this surface handle the kinds I wrote
// down for it?". None of them answers "do the surfaces agree about which kinds
// exist?", and that is the question that keeps being wrong here. assistant_reset
// reached one of four surfaces; subagent_begin was relayed across processes and
// absent from this process's own --json; a kind was added to the relay's
// allowlist with no arm in the renderer, and later an arm with no allowlist
// entry. Each shipped green because each surface was tested against the list
// its own author remembered.
//
// The set of kinds comes from agent.AllEventKinds, which is itself checked
// against the parsed source of the constant block, so a kind cannot be added
// to the module and stay invisible to this table.
//
// A surface that deliberately ignores a kind declares it in
// .mivia/policy/viewer-surfaces.json WITH A REASON. Silence is not a
// declaration: an undeclared kind that produces nothing fails.

// viewerSurfacePolicy is the on-disk declaration of deliberate silence.
type viewerSurfacePolicy struct {
	Surfaces map[string]struct {
		Ignores map[string]string `json:"ignores"`
	} `json:"surfaces"`
}

const viewerSurfacePolicyPath = "../../.mivia/policy/viewer-surfaces.json"

func loadViewerSurfacePolicy(t *testing.T) viewerSurfacePolicy {
	t.Helper()
	data, err := os.ReadFile(viewerSurfacePolicyPath)
	if err != nil {
		t.Fatalf("read %s: %v", viewerSurfacePolicyPath, err)
	}
	var p viewerSurfacePolicy
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("parse %s: %v", viewerSurfacePolicyPath, err)
	}
	if len(p.Surfaces) == 0 {
		t.Fatalf("%s declares no surfaces; the policy is wrong, not the code",
			viewerSurfacePolicyPath)
	}
	return p
}

// conformanceEvent builds an event of kind k carrying every field a surface
// could render, so a surface that produces nothing is silent by DECISION and
// not for want of content.
func conformanceEvent(k agent.EventKind) agent.Event {
	ev := agent.Event{
		Kind:       k,
		Content:    "some content",
		Detail:     "some detail",
		Name:       "some_name",
		ToolCallID: "call-1",
		Input:      "some input",
		Output:     "some output",
		Status:     "completed",
	}
	switch k {
	case agent.EventCompaction:
		ev.Compaction = &events.CompactionEvent{Trigger: "manual"}
	case agent.EventCacheUsage:
		ev.CacheUsage = &events.CacheUsageEvent{Provider: "p", Model: "m"}
	case agent.EventTokenUsage:
		ev.TokenUsage = &events.TokenUsageEvent{Provider: "p", Model: "m"}
	}
	// Subagent kinds mean nothing without a run to attribute them to, and
	// several surfaces drop an unattributed one on purpose.
	switch k {
	case agent.EventSubagentBegin, agent.EventSubagentDone,
		agent.EventSubagentHeartbeat, agent.EventSubagentStart, agent.EventSubagentEnd:
		ev.Origin = agent.EventOrigin{TaskID: "task-1", Agent: "reviewer", Depth: 1}
	}
	return ev
}

// producesOnTUI reports whether the TUI translation yields any UI event.
func producesOnTUI(ev agent.Event) bool {
	return len(uiadapter.TranslateEventWithOptions(ev, uiadapter.TranslateOptions{})) > 0
}

// producesOnJSON reports whether --json line mode writes any NDJSON line.
func producesOnJSON(ev agent.Event) bool {
	var buf bytes.Buffer
	jsonTurnEventCallback(&buf)(ev)
	return buf.Len() > 0
}

// producesOnSync reports whether the chat-sync projector - the surface the web
// app reads - emits a wire event for the equivalent bus event.
//
// It was missing from this table while the table's own commit message claimed
// to close the class. The projector's projectByKind ends in a bare `default:
// return nil`, so a kind could be minted, relayed, rendered on the three
// surfaces below, and dropped in silence by the one a remote viewer reads,
// with every suite green.
func producesOnSync(ev agent.Event) bool {
	p := chatsync.NewProjector("sess-1", 0, chatsync.ProjectorOptions{
		StreamAssistant: true, IncludeThinking: true, IncludeToolIO: true,
	})
	// Open the turn: the projector drops content for a turn it has not seen,
	// which would make every kind look unhandled.
	p.Project(events.Event{
		Kind: events.KindTurnStart, SessionID: "sess-1", TurnID: "turn:1",
		Detail: "the prompt", Timestamp: time.Now(),
	})

	busEv := events.Event{
		Kind: events.Kind(ev.Kind), SessionID: "sess-1", TurnID: "turn:1",
		Content: ev.Content, Detail: ev.Detail, Name: ev.Name,
		ToolCallID: ev.ToolCallID, Input: ev.Input, Output: ev.Output,
		Timestamp: time.Now(),
	}
	// The typed payloads the projector requires, mirrored from the agent event.
	busEv.Compaction = ev.Compaction
	if ev.Kind == agent.EventHook {
		busEv.Hook = &events.HookEvent{
			Phase: ev.Name, Program: ev.Program, Tool: ev.Tool,
			Denied: ev.Denied, Output: ev.Output,
		}
	}
	if ev.Origin.TaskID != "" {
		busEv = busEv.WithAgentAttribution(ev.Origin.TaskID, ev.Origin.Agent, ev.Origin.Depth)
	}
	return len(p.Project(busEv)) > 0
}

// producesOnRelay reports whether the cross-process NDJSON renderer writes a
// line for the equivalent bus event. It drives the real dispatcher, so routing
// between the root and subagent arms is exercised rather than bypassed.
func producesOnRelay(ev agent.Event) bool {
	var buf bytes.Buffer
	state := newExternalTurnState()
	busEv := events.Event{
		Kind: events.Kind(ev.Kind), SessionID: "s1", TurnID: "run-1",
		Content: ev.Content, Detail: ev.Detail, Name: ev.Name,
		ToolCallID: ev.ToolCallID, Input: ev.Input, Output: ev.Output,
	}
	if ev.Origin.TaskID != "" {
		busEv = busEv.WithAgentAttribution(ev.Origin.TaskID, ev.Origin.Agent, ev.Origin.Depth)
	}
	// Open the run first: a relayed event for a run the receiver has never
	// seen is dropped by design, which would make every kind look unhandled.
	renderExternalEvent(&buf, state, events.Event{
		Kind: events.KindTurnStart, SessionID: "s1", TurnID: "run-1", Detail: "the task",
	})
	before := buf.Len()
	renderExternalEvent(&buf, state, busEv)
	return buf.Len() > before
}

// TestEveryEventKindReachesEveryViewerOrSaysWhyNot is the table.
func TestEveryEventKindReachesEveryViewerOrSaysWhyNot(t *testing.T) {
	policy := loadViewerSurfacePolicy(t)
	surfaces := map[string]func(agent.Event) bool{
		"tui":   producesOnTUI,
		"json":  producesOnJSON,
		"relay": producesOnRelay,
		"sync":  producesOnSync,
	}

	for name := range surfaces {
		if _, declared := policy.Surfaces[name]; !declared {
			t.Fatalf("surface %q has no entry in %s; a surface with no policy entry "+
				"cannot declare a deliberate silence", name, viewerSurfacePolicyPath)
		}
	}

	kinds := agent.AllEventKinds()
	if len(kinds) == 0 {
		t.Fatal("agent.AllEventKinds is empty; the registry is wrong, not this test")
	}

	for _, surfaceName := range sortedKeys(surfaces) {
		produces := surfaces[surfaceName]
		ignores := policy.Surfaces[surfaceName].Ignores
		for _, k := range kinds {
			kind, name := k, fmt.Sprintf("%s/%s", surfaceName, k)
			t.Run(name, func(t *testing.T) {
				reason, declared := ignores[string(kind)]
				got := produces(conformanceEvent(kind))
				switch {
				case got && declared:
					t.Errorf("%s renders %s, but the policy declares it ignored (%q). "+
						"A stale declaration hides the next kind that really is dropped.",
						surfaceName, kind, reason)
				case !got && !declared:
					t.Errorf("%s produces nothing for %s and does not declare it. "+
						"Either give the surface an arm, or add it to %s with a reason - "+
						"this is exactly how assistant_reset reached one viewer of four.",
						surfaceName, kind, viewerSurfacePolicyPath)
				case !got && declared && strings.TrimSpace(reason) == "":
					t.Errorf("%s declares %s ignored with an empty reason; a declaration "+
						"that says nothing is a silence with extra steps", surfaceName, kind)
				}
			})
		}
	}
}

// TestTheRelayAllowlistAndItsRendererAgree closes the gap between the two
// halves of cross-process delivery.
//
// A kind must be BOTH in hub.RelayedKinds (or it never crosses the process
// boundary) AND have an arm in the renderer (or it is dropped after it does).
// Both halves have shipped broken independently: a kind was added to the
// allowlist with no arm, and later an arm was added whose allowlist entry
// nothing tested. Each half's own test passed both times, because each tested
// its own half.
func TestTheRelayAllowlistAndItsRendererAgree(t *testing.T) {
	relayed := map[events.Kind]bool{}
	for _, k := range hub.RelayedKinds() {
		relayed[k] = true
	}
	if len(relayed) == 0 {
		t.Fatal("hub.RelayedKinds is empty; this test proves nothing")
	}

	for _, k := range agent.AllEventKinds() {
		kind := events.Kind(k)
		renders := producesOnRelay(conformanceEvent(k))
		switch {
		case renders && !relayed[kind]:
			t.Errorf("the relay renderer has an arm for %s but hub.RelayedKinds omits it, "+
				"so the event never crosses the process boundary and the arm is dead", kind)
		case !renders && relayed[kind]:
			t.Errorf("hub.RelayedKinds carries %s but the renderer has no arm, so it is "+
				"dropped in silence after crossing the process boundary", kind)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
