package chatsync

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// dispatchedToolEvent is toolEvent's lane twin: a tool call made BY a
// subagent, which closes that run's blocks and not the root transcript's.
func dispatchedToolEvent(kind events.Kind, task, callID string) events.Event {
	ev := events.Event{
		Kind:       kind,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		Timestamp:  time.Now(),
		ToolCallID: callID,
		Name:       "Read",
	}
	return ev.WithAgentAttribution(task, "builder", 1)
}

// TestALaneThinkingTailShipsUnderTheSubagentType is the lane half of
// flushHeldThinking.
//
// The root half is covered; the lane half chooses a different wire type from
// the same code, and the type is the whole contract. A viewer keeps subagent
// output out of the main transcript on the type prefix alone
// (TestSubagentProseUsesItsOwnTypes), so a lane's held reasoning tail shipped
// under the ROOT thinking type would splice one run's reasoning into the
// operator's own transcript - and there is no way for the consumer to tell.
func TestALaneThinkingTailShipsUnderTheSubagentType(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	// Two reasoning fragments long enough to clear the hold-back, so deltas
	// ship (thinkingStreamed) and a tail stays behind.
	long := strings.Repeat("weighing the options here. ", 20)
	tail := "and that settles it."
	for _, text := range []string{long, long, tail} {
		p.Project(subagentEvent(events.KindThinking, "task-1", text, "delta"))
	}
	ls := p.lanes["turn:1\x00task-1"]
	if ls == nil || !ls.thinkingStreamed || !ls.thinkingStream.Pending() {
		t.Fatalf("precondition: lane state %+v is not a streamed lane holding a thinking tail", ls)
	}

	out := p.Project(dispatchedToolEvent(events.KindToolStart, "task-1", "call-1"))

	var flushIndex, settleIndex = -1, -1
	var flushed string
	var aggregate *SubagentThinkingMessagePayload
	for i, we := range out {
		switch payload := we.Payload.(type) {
		case *SubagentThinkingDeltaPayload:
			flushIndex, flushed = i, payload.Text
		case *SubagentThinkingMessagePayload:
			settleIndex, aggregate = i, payload
		case *ThinkingDeltaPayload, *ThinkingMessagePayload:
			t.Fatalf("a subagent's reasoning reached the wire under the ROOT type %s; a viewer files "+
				"that into the operator's own transcript", we.Type)
		}
	}
	if flushIndex < 0 {
		t.Fatalf("the lane's held reasoning tail never shipped:\n%s", describeWire(out))
	}
	if settleIndex < 0 {
		t.Fatalf("the lane's thinking block never settled:\n%s", describeWire(out))
	}
	if flushIndex > settleIndex {
		t.Errorf("the tail delta is at index %d, after the aggregate at %d; a viewer renders in arrival "+
			"order and would append the tail below the closed block", flushIndex, settleIndex)
	}
	if !strings.Contains(flushed, tail) {
		t.Errorf("the flushed tail = %q, want it to carry %q - the last reasoning fragment was withheld "+
			"by the redactor and this delta is the only thing that releases it", flushed, tail)
	}
	if aggregate.Text != "" {
		t.Errorf("the aggregate carried Text = %q; deltas shipped, so INV-1 requires it empty or the "+
			"viewer stitches the reasoning twice", aggregate.Text)
	}
	if aggregate.Fragments != ls.thinkingFragments {
		t.Errorf("the aggregate claims %d fragments, the lane counted %d; the flush increments the "+
			"count it reports", aggregate.Fragments, ls.thinkingFragments)
	}
}

// The two arms of flushHeldAssistantOnProseEnd that REFUSE.
//
// The function's whole job is to release a held prose tail only on an event
// that PROVES the content block ended. Its three callers each pass exactly one
// proving kind, so the refusals never run through Project - which is precisely
// why they must be pinned here: a fourth caller, or a widened switch, would
// otherwise start flushing a tail mid-utterance with nothing failing.
//
// A tail released early is not cosmetic. The hold-back is what lets the
// cross-fragment redactor catch a secret split across two deltas; shipping the
// window before the block is over is the leak that hold-back exists to stop.
func TestTheProseEndFlushRefusesEventsThatDoNotProveTheBlockEnded(t *testing.T) {
	streamPolicy(t)

	for name, ev := range map[string]events.Event{
		// A delta is mid-utterance by definition: more of this block follows.
		"an assistant delta":  rootEvent(events.KindAssistant, "more to come", "delta"),
		"a bare assistant":    rootEvent(events.KindAssistant, "", ""),
		"a tool start":        toolEvent(events.KindToolStart, "call-1"),
		"a subagent terminal": rootEvent(events.KindSubagentDone, "", "completed"),
	} {
		t.Run(name, func(t *testing.T) {
			p := NewProjector("sess-1", 0, proseOpts())
			held := "the first half of a secret xk-tok-"
			if out := p.Project(rootEvent(events.KindAssistant, held, "delta")); len(out) != 0 {
				t.Fatalf("the seeded delta shipped %d events; it must be held whole", len(out))
			}
			ts := p.turn("turn:1")
			if !ts.assistantStream.Pending() {
				t.Fatal("precondition: nothing is held back, so a wrongly permissive flush would emit nothing anyway")
			}

			got := p.flushHeldAssistantOnProseEnd(p.buildEnvelope(ev, "turn:1"), "turn:1", ev)

			if len(got) != 0 {
				t.Fatalf("%s released the held tail as %d wire events; only a thinking fragment, a hook "+
					"or the loop's message-complete flag proves the prose block ended:\n%s",
					name, len(got), describeWire(got))
			}
			if !ts.assistantStream.Pending() {
				t.Fatalf("%s emitted nothing but still drained the hold-back buffer; the withheld bytes "+
					"are gone and no later flush can ship them", name)
			}
		})
	}
}

// TestTheProseEndFlushReleasesOnAProvingEvent is the positive control for the
// test above: with the same held tail, an event that DOES prove the block
// ended ships it. Without this, a flush that refused everything would pass the
// refusal cases and lose every tail in production.
func TestTheProseEndFlushReleasesOnAProvingEvent(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())
	held := "the whole short answer"
	p.Project(rootEvent(events.KindAssistant, held, "delta"))

	ev := rootEvent(events.KindAssistant, "", events.DetailAssistantComplete)
	got := p.flushHeldAssistantOnProseEnd(p.buildEnvelope(ev, "turn:1"), "turn:1", ev)
	if shippedText(got) != held {
		t.Fatalf("the message-complete flag shipped %q, want the held tail %q", shippedText(got), held)
	}
}
