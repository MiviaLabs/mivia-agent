package clichat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/hub"
)

// relayLines drives the real dispatcher, not one branch of it, and returns one
// decoded NDJSON line per written event. Calling renderExternalTurnEvent
// directly would bypass the routing that decides root from subagent - the
// thing most of these tests exist to check.
func relayLines(t *testing.T, evs ...events.Event) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	state := newExternalTurnState()
	for _, ev := range evs {
		renderExternalEvent(&buf, state, ev)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("relayed line is not JSON: %v (%q)", err, line)
		}
		out = append(out, m)
	}
	return out
}

func attributed(kind events.Kind, content, detail string) events.Event {
	ev := events.Event{
		Kind: kind, SessionID: "s1", TurnID: "run-1", Content: content, Detail: detail,
	}
	return ev.WithAgentAttribution("task-1", "builder", 1)
}

func rootEv(kind events.Kind, content, detail string) events.Event {
	return events.Event{
		Kind: kind, SessionID: "s1", TurnID: "run-1", Content: content, Detail: detail,
	}
}

func typesOf(lines []map[string]any) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l["type"].(string))
	}
	return out
}

// TestSubagentOutputNeverUsesARootType is the contract in one assertion.
//
// It drives every kind the hub actually relays, rather than a list written by
// hand here, so a kind added to the relay without a subagent arm fails this
// test instead of silently reaching a consumer under a root type.
func TestSubagentOutputNeverUsesARootType(t *testing.T) {
	rootTypes := map[string]bool{
		"external_chunk": true, "external_thinking": true,
		"external_tool_start": true, "external_tool_end": true,
	}

	var relayed int
	for _, kind := range hub.RelayedKinds() {
		if externalSubagentType(kind) == "" {
			// Not a kind a subagent can produce (turn lifecycle, compaction).
			continue
		}
		lines := relayLines(t,
			rootEv(events.KindTurnStart, "", "the task"),
			attributed(kind, "some content", "some detail"),
		)
		for _, line := range lines {
			lineType := line["type"].(string)
			if lineType == "external_turn_start" {
				continue
			}
			relayed++
			if rootTypes[lineType] {
				t.Errorf("kind %s relayed as %s, a ROOT type - a consumer keyed on "+
					"type splices this into the root agent's own output", kind, lineType)
			}
			if !strings.HasPrefix(lineType, "external_subagent_") {
				t.Errorf("kind %s relayed as %s, which carries no subagent marker", kind, lineType)
			}
		}
	}
	if relayed == 0 {
		t.Fatal("no subagent line was relayed at all; the test proved nothing")
	}
}

// TestUnstampedSubagentKindStillRelaysAsSubagent covers the peer that relays a
// subagent kind without stamping an origin. Attribution alone is not a safe
// predicate: the subagent kinds are subagent by construction, so a narrower
// check would render an unstamped peer's subagent tool calls as the root
// agent's own.
func TestUnstampedSubagentKindStillRelaysAsSubagent(t *testing.T) {
	lines := relayLines(t,
		rootEv(events.KindTurnStart, "", "the task"),
		events.Event{
			Kind: events.KindSubagentStart, SessionID: "s1", TurnID: "run-1",
			ToolCallID: "c1", Name: "read_file",
		},
	)

	got := typesOf(lines)
	if !containsString(got, "external_subagent_tool_start") {
		t.Fatalf("unstamped subagent kind relayed as %v, want a subagent type", got)
	}
}

// TestSubagentLinesCarryTheirRunIdentity proves the type says WHO produced the
// line and the origin fields say WHICH RUN. Two runs of one agent share a name
// but not a task id, so the fields are what tell them apart.
func TestSubagentLinesCarryTheirRunIdentity(t *testing.T) {
	lines := relayLines(t,
		rootEv(events.KindTurnStart, "", "the task"),
		attributed(events.KindAssistant, "sub text", ""),
	)

	for _, line := range lines {
		if line["type"] != "external_subagent_chunk" {
			continue
		}
		if line["origin_task_id"] != "task-1" || line["origin_agent"] != "builder" {
			t.Errorf("subagent line lost its run identity: %v", line)
		}
		return
	}
	t.Fatalf("no subagent chunk was relayed: %v", typesOf(lines))
}

// TestRootLinesCarryNoOriginFields proves the root agent's own lines stay
// exactly as they were. After the split the origin fields are reachable only
// on subagent types, so their presence on a root line would be meaningless.
func TestRootLinesCarryNoOriginFields(t *testing.T) {
	lines := relayLines(t,
		rootEv(events.KindTurnStart, "", "the task"),
		rootEv(events.KindAssistant, "root text", "delta"),
	)

	for _, line := range lines {
		if line["type"] != "external_chunk" {
			continue
		}
		for _, field := range []string{"origin_task_id", "origin_agent", "origin_depth"} {
			if _, present := line[field]; present {
				t.Errorf("root line carries %s; the root agent has no origin", field)
			}
		}
		return
	}
	t.Fatalf("no root chunk was relayed: %v", typesOf(lines))
}

// TestSubagentOutputDoesNotSuppressTheRootAggregate is the regression the
// split makes structurally impossible.
//
// The run's streamed flag exists so a run that already got live deltas is not
// sent the same text again as a turn-end aggregate. A subagent's delta setting
// that flag made the relay drop the ROOT's aggregate, and since the root never
// streamed, the consumer was left with no root text at all. The subagent path
// now has no access to that state.
func TestSubagentOutputDoesNotSuppressTheRootAggregate(t *testing.T) {
	lines := relayLines(t,
		rootEv(events.KindTurnStart, "", "the task"),
		attributed(events.KindAssistant, "sub chunk", "delta"),
		rootEv(events.KindAssistant, "root answer", ""),
	)

	var sawRoot bool
	for _, line := range lines {
		if line["text"] == "root answer" && line["type"] == "external_chunk" {
			sawRoot = true
		}
	}
	if !sawRoot {
		t.Fatalf("the root aggregate was suppressed by a subagent's delta: %v", typesOf(lines))
	}
}

// TestRootStreamingDoesNotSuppressASubagentAggregate is the mirror case.
func TestRootStreamingDoesNotSuppressASubagentAggregate(t *testing.T) {
	lines := relayLines(t,
		rootEv(events.KindTurnStart, "", "the task"),
		rootEv(events.KindAssistant, "root chunk", "delta"),
		attributed(events.KindAssistant, "sub answer", ""),
	)

	var sawSub bool
	for _, line := range lines {
		if line["text"] == "sub answer" && line["type"] == "external_subagent_chunk" {
			sawSub = true
		}
	}
	if !sawSub {
		t.Fatalf("a subagent's answer was suppressed by the root's streamed flag: %v", typesOf(lines))
	}
}

// TestSubagentRunLifecycleReachesTheRelay covers the kinds that were relayed
// by the hub and had no arm in the renderer at all, so they were dropped in
// silence after crossing the process boundary.
//
// The terminal matters most: without it a relayed run opened and never closed,
// so a live-agents view built on the opening signal pinned every subagent of
// the session forever.
func TestSubagentRunLifecycleReachesTheRelay(t *testing.T) {
	lines := relayLines(t,
		rootEv(events.KindTurnStart, "", "the task"),
		attributed(events.KindSubagentBegin, "", "review the diff"),
		attributed(events.KindSubagentHeartbeat, "", "elapsed=2s steps=1"),
		attributed(events.KindSubagentDone, "", "completed"),
	)

	got := typesOf(lines)
	for _, want := range []string{
		"external_subagent_begin", "external_subagent_heartbeat", "external_subagent_done",
	} {
		if !containsString(got, want) {
			t.Errorf("%s never reached the relay; got %v", want, got)
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
