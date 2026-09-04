package chatsync

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// A subagent terminal that carries NO attribution.
//
// KindSubagentDone reaches projectByKind's subagent arm whether or not the
// event names the run it ends. An unattributed one is not hypothetical: the
// bounded queues that carry subagent events shed attribution-bearing frames,
// and the dispatcher emits run lifecycle events for panels and referrals that
// never travelled through a lane at all.
//
// Three separate guards all key on that same fact, and none of them ran:
//
//   - flushHeldAssistantOnStepClose refuses to flush,
//   - settleThinkingOnStepClose refuses to settle, and
//   - retireLane refuses to retire.
//
// Without them, one subagent's terminal closes the ROOT transcript's prose
// and reasoning blocks - publishing an aggregate for a block the root loop has
// not finished speaking - and retireLane("") would scan every lane for the
// suffix "\x00", which no key can end with, so it is inert only by accident.

// TestAnUnattributedSubagentTerminalClosesNoBlock pins all three refusals in
// the one Project call that reaches them.
func TestAnUnattributedSubagentTerminalClosesNoBlock(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	// A live lane, so the retire guard has something it could wrongly forget.
	p.Project(subagentEvent(events.KindAssistant, "task-1", strings.Repeat("lane words ", 40), "delta"))
	// The root turn: reasoning first (it would flush the prose tail), then a
	// short prose delta that the hold-back keeps whole.
	p.Project(rootEvent(events.KindThinking, "let me think about this", "delta"))
	if out := p.Project(rootEvent(events.KindAssistant, "a short answer", "delta")); len(out) != 0 {
		t.Fatalf("the short prose delta shipped %d events; the hold-back must keep it whole for this test to mean anything", len(out))
	}

	out := p.Project(rootEvent(events.KindSubagentDone, "", "completed"))

	if len(out) != 1 {
		t.Fatalf("an unattributed subagent terminal produced %d wire events, want 1 (subagent.ended alone):\n%s",
			len(out), describeWire(out))
	}
	if out[0].Type != TypeSubagentEnded {
		t.Fatalf("event type = %s, want %s", out[0].Type, TypeSubagentEnded)
	}
	if _, ok := p.lanes["turn:1\x00task-1"]; !ok {
		t.Error("the live lane was retired by a terminal that named no run; its next aggregate would " +
			"re-ship every word the viewer already received delta by delta")
	}

	// The root's held tail is still held, not lost: the guard declined to
	// flush it, so the next event that PROVES the block ended still ships it.
	flushed := p.Project(rootEvent(events.KindHook, "", "stop"))
	if got := shippedText(flushed); !strings.Contains(got, "a short answer") {
		t.Errorf("the held prose tail = %q after a hook, want it to contain the answer: "+
			"declining to flush must delay the tail, never drop it", got)
	}
}

// TestRetiringOneRunKeepsEveryOtherLane is the positive half of the retire
// guard: an ATTRIBUTED terminal forgets exactly one run's state and carries
// every other lane through untouched.
//
// The carry-through is what the loop's kept slice does, and it is the half a
// mistake makes silent: a lane wrongly dropped is recreated on its next delta
// with streamed=false, and its aggregate then re-ships the whole answer the
// viewer already holds.
func TestRetiringOneRunKeepsEveryOtherLane(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(subagentEvent(events.KindAssistant, "task-1", "first run says this", "delta"))
	p.Project(subagentEvent(events.KindAssistant, "task-2", "second run says that", "delta"))

	p.Project(subagentEvent(events.KindSubagentDone, "task-1", "", "completed"))

	if _, ok := p.lanes["turn:1\x00task-1"]; ok {
		t.Error("the finished run's lane survived its own terminal; finished runs then crowd out live ones under the LRU")
	}
	if _, ok := p.lanes["turn:1\x00task-2"]; !ok {
		t.Fatal("the OTHER run's lane was retired by task-1's terminal")
	}
	if want := []string{"turn:1\x00task-2"}; len(p.laneOrder) != 1 || p.laneOrder[0] != want[0] {
		t.Fatalf("laneOrder = %q, want %q: the surviving key must stay in the LRU order too, "+
			"or the map and the order disagree about which lanes exist", p.laneOrder, want)
	}

	// The survivor's counters came through with it, so its aggregate reports
	// what the viewer already has instead of re-shipping it.
	out := p.Project(subagentEvent(events.KindAssistant, "task-2", "second run says that", ""))
	if len(out) != 1 {
		t.Fatalf("the surviving lane's aggregate produced %d events, want 1", len(out))
	}
	payload, ok := out[0].Payload.(*SubagentAssistantMessagePayload)
	if !ok {
		t.Fatalf("aggregate payload is %T, want *SubagentAssistantMessagePayload", out[0].Payload)
	}
	if payload.Fragments != 1 || payload.Text != "" {
		t.Errorf("Fragments = %d, Text = %q; want 1 and empty - the lane's delta is on the wire "+
			"and INV-1 must not send the same words twice", payload.Fragments, payload.Text)
	}
}

// describeWire renders a wire batch as types for failure messages.
func describeWire(evs []WireEvent) string {
	var b strings.Builder
	for _, we := range evs {
		b.WriteString("  " + we.Type + "\n")
	}
	return b.String()
}
