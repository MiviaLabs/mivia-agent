package clichat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// relayLines renders the events through the external relay and returns one
// decoded NDJSON line per written event.
func relayLines(t *testing.T, evs ...events.Event) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	r := &externalRun{}
	for _, ev := range evs {
		renderExternalTurnEvent(&buf, r, ev)
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
	ev := events.Event{Kind: kind, TurnID: "run-1", Content: content, Detail: detail}
	return ev.WithAgentAttribution("task-1", "builder", 1)
}

// TestExternalRelayAttributesSubagentProse proves a relayed surface can tell a
// subagent's text from the root turn's. The relay carries every kind on one
// NDJSON stream keyed by run_id, so without the origin fields a consumer reads
// a subagent's prose as the root turn's own output.
func TestExternalRelayAttributesSubagentProse(t *testing.T) {
	lines := relayLines(t,
		attributed(events.KindAssistant, "sub text", "delta"),
		attributed(events.KindThinking, "sub reasoning", ""),
	)

	if len(lines) != 2 {
		t.Fatalf("relayed %d lines, want 2", len(lines))
	}
	for _, line := range lines {
		if line["origin_task_id"] != "task-1" {
			t.Errorf("%s: origin_task_id = %v, want task-1", line["type"], line["origin_task_id"])
		}
		if line["origin_agent"] != "builder" {
			t.Errorf("%s: origin_agent = %v, want builder", line["type"], line["origin_agent"])
		}
	}
}

// TestExternalRelayLeavesRootLinesUnattributed proves the root loop's own
// lines carry no origin fields, so a consumer reading their presence as "this
// came from a subagent" stays correct.
func TestExternalRelayLeavesRootLinesUnattributed(t *testing.T) {
	lines := relayLines(t, events.Event{
		Kind: events.KindAssistant, TurnID: "run-1", Content: "root text", Detail: "delta",
	})

	if len(lines) != 1 {
		t.Fatalf("relayed %d lines, want 1", len(lines))
	}
	for _, field := range []string{"origin_task_id", "origin_agent", "origin_depth"} {
		if _, present := lines[0][field]; present {
			t.Errorf("root line carries %s; it must be omitted for the root loop", field)
		}
	}
}

// TestSubagentDeltaDoesNotSuppressRootAggregate is the relay's version of the
// projector's blank-message bug.
//
// The run's streamed flag exists so a run that already got live deltas does
// not receive the same text again as a turn-end aggregate. A subagent's delta
// setting that flag makes the relay drop the ROOT's aggregate - and since the
// root never streamed, the consumer ends up with no root text at all.
func TestSubagentDeltaDoesNotSuppressRootAggregate(t *testing.T) {
	lines := relayLines(t,
		attributed(events.KindAssistant, "sub chunk", "delta"),
		events.Event{Kind: events.KindAssistant, TurnID: "run-1", Content: "root answer"},
	)

	var sawRoot bool
	for _, line := range lines {
		if line["text"] == "root answer" {
			sawRoot = true
		}
	}
	if !sawRoot {
		t.Fatalf("root aggregate was suppressed by a subagent's delta; relayed %v", lines)
	}
}

// TestSubagentAggregateRelayedDespiteRootStreaming is the mirror case: the
// root streams, then a subagent settles. The root's streamed flag must not
// consume a DIFFERENT producer's aggregate.
func TestSubagentAggregateRelayedDespiteRootStreaming(t *testing.T) {
	lines := relayLines(t,
		events.Event{Kind: events.KindAssistant, TurnID: "run-1", Content: "root chunk", Detail: "delta"},
		attributed(events.KindAssistant, "sub answer", ""),
	)

	var sawSub bool
	for _, line := range lines {
		if line["text"] == "sub answer" {
			sawSub = true
		}
	}
	if !sawSub {
		t.Fatalf("subagent aggregate was suppressed by the root's streamed flag; relayed %v", lines)
	}
}
