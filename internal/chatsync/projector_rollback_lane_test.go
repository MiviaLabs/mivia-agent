package chatsync

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// RollbackStreaming's LANE arms, and the guards that stop it corrupting state
// it cannot find.
//
// A projected batch that the outbox never stored has to be un-projected, or
// the producer's counters describe what this side intended to send rather than
// what the viewer holds - and INV-1 then empties a settled aggregate whose
// deltas never arrived, losing the reply outright. The root arms of that undo
// are covered; the subagent arms are the same defect one lane over, where it
// is harder to see because a run's output has its own transcript.

// laneAggregate projects the lane's turn-level aggregate and returns it.
func laneAggregate(t *testing.T, p *Projector, task, text string) *SubagentAssistantMessagePayload {
	t.Helper()
	out := p.Project(subagentEvent(events.KindAssistant, task, text, ""))
	if len(out) != 1 {
		t.Fatalf("the lane aggregate produced %d wire events, want 1:\n%s", len(out), describeWire(out))
	}
	payload, ok := out[0].Payload.(*SubagentAssistantMessagePayload)
	if !ok {
		t.Fatalf("aggregate payload is %T, want *SubagentAssistantMessagePayload", out[0].Payload)
	}
	return payload
}

// TestALostLaneDeltaMakesTheLaneAggregateCarryTheAnswer is the delta arm. The
// lane streamed one delta and it was never stored, so nothing of that run's
// answer reached the viewer: the aggregate must carry the whole text. Leaving
// the counters where the projection put them makes the aggregate report a
// fragment the viewer never got and then empty its own text under INV-1 - the
// subagent's reply disappears while its transcript still looks complete.
func TestALostLaneDeltaMakesTheLaneAggregateCarryTheAnswer(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	answer := "the run's whole answer"

	lost := p.Project(subagentEvent(events.KindAssistant, "task-1", answer, "delta"))
	if len(lost) != 1 {
		t.Fatalf("the lane delta produced %d wire events, want 1", len(lost))
	}
	if _, ok := lost[0].Payload.(*SubagentAssistantDeltaPayload); !ok {
		t.Fatalf("payload is %T, want *SubagentAssistantDeltaPayload", lost[0].Payload)
	}
	// The outbox never stored it.
	p.RollbackStreaming(lost)

	ls := p.lanes["turn:1\x00task-1"]
	if ls == nil {
		t.Fatal("the lane state vanished")
	}
	if ls.fragments != 0 || ls.streamed {
		t.Errorf("after the undo the lane reports fragments=%d streamed=%v, want 0 and false: "+
			"the viewer received nothing", ls.fragments, ls.streamed)
	}

	payload := laneAggregate(t, p, "task-1", answer)
	if payload.Fragments != 0 {
		t.Errorf("Fragments = %d, want 0 - a non-zero count empties the text under INV-1 and claims "+
			"a delta the viewer never got", payload.Fragments)
	}
	if payload.Text != answer {
		t.Errorf("Text = %q, want the whole answer %q - this aggregate is the only copy left", payload.Text, answer)
	}
}

// TestALostLaneSettleIsRetried is the per-block settle arm. The lane's
// message-complete flag settled its block and that batch was never stored, so
// the block must NOT stay marked settled: nothing else would retry it, and the
// block would wait for the whole turn to end before any aggregate named it.
func TestALostLaneSettleIsRetried(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	long := strings.Repeat("the run reports at length. ", 20)

	p.Project(subagentEvent(events.KindAssistant, "task-1", long, "delta"))
	lost := p.Project(subagentEvent(events.KindAssistant, "task-1", "", events.DetailAssistantComplete))
	if len(lost) != 1 {
		t.Fatalf("the lane settle produced %d wire events, want 1:\n%s", len(lost), describeWire(lost))
	}
	settled, ok := lost[0].Payload.(*SubagentAssistantMessagePayload)
	if !ok {
		t.Fatalf("payload is %T, want *SubagentAssistantMessagePayload", lost[0].Payload)
	}

	// A second flag on the same block settles nothing while the first stands.
	if again := p.Project(subagentEvent(events.KindAssistant, "task-1", "", events.DetailAssistantComplete)); len(again) != 0 {
		t.Fatalf("a repeated complete flag re-settled a stored block as %d events; the block is already on the wire", len(again))
	}

	p.RollbackStreaming(lost)

	retry := p.Project(subagentEvent(events.KindAssistant, "task-1", "", events.DetailAssistantComplete))
	if len(retry) != 1 {
		t.Fatalf("after the settle was rolled back a later flag produced %d wire events, want 1: "+
			"a settle that never reached the wire must be retried, or the block waits for the turn's end", len(retry))
	}
	retried, ok := retry[0].Payload.(*SubagentAssistantMessagePayload)
	if !ok {
		t.Fatalf("retry payload is %T, want *SubagentAssistantMessagePayload", retry[0].Payload)
	}
	if retried.Block != settled.Block {
		t.Errorf("the retry named block %q, the lost settle named %q; the retry has to repair the SAME block",
			retried.Block, settled.Block)
	}
}

// TestRollingBackAnUnknownStreamChangesNothing pins the nil guards on all four
// undo helpers.
//
// The state a rollback names can be gone. Lanes are bounded by
// maxTrackedLanes and evicted in LRU order while a run is still producing, and
// a rebase or a fork replaces the projector's turn map, so a batch handed back
// by a failed append can easily reference a stream this projector no longer
// holds. Two things must then hold: no panic - this runs on the session's
// worker goroutine, and a panic there takes the host process down over a disk
// error the caller already handles - and no state CREATED, because a rollback
// that allocated the turn it was undoing would mint a fresh segment id and
// leave a turn that never existed looking live.
func TestRollingBackAnUnknownStreamChangesNothing(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	// One real turn, so "no state created" is a claim about the maps' contents
	// rather than about an empty projector.
	p.Project(rootEvent(events.KindAssistant, "a real turn", "delta"))
	wantTurns, wantLanes, wantSegments := len(p.turns), len(p.lanes), p.nextSegment

	ghost := Envelope{V: 1, Turn: "turn:gone"}
	ghostLane := Envelope{V: 1, Turn: "turn:gone", Agent: &AgentOrigin{Task: "task-gone", Depth: 1}}
	p.RollbackStreaming([]WireEvent{
		{Type: TypeAssistantDelta, Payload: &AssistantDeltaPayload{Envelope: ghost, Text: "x"}},
		{Type: TypeSubagentAssistantDelta, Payload: &SubagentAssistantDeltaPayload{Envelope: ghostLane, Text: "x"}},
		{Type: TypeThinkingDelta, Payload: &ThinkingDeltaPayload{Envelope: ghost, Text: "x"}},
		{Type: TypeSubagentThinkingDelta, Payload: &SubagentThinkingDeltaPayload{Envelope: ghostLane, Text: "x"}},
		{Type: TypeAssistantMessage, Payload: &AssistantMessagePayload{Envelope: ghost}},
		{Type: TypeSubagentAssistantMessage, Payload: &SubagentAssistantMessagePayload{Envelope: ghostLane}},
		{Type: TypeAssistantReset, Payload: &AssistantResetPayload{Envelope: ghost}},
		{Type: TypeAssistantReset, Payload: &AssistantResetPayload{Envelope: ghostLane}},
	})

	if len(p.turns) != wantTurns || len(p.lanes) != wantLanes {
		t.Errorf("the undo left %d turns and %d lanes, want %d and %d: rolling back a stream this "+
			"projector no longer holds must not resurrect it",
			len(p.turns), len(p.lanes), wantTurns, wantLanes)
	}
	if p.nextSegment != wantSegments {
		t.Errorf("the undo minted segment ids (nextSegment %d -> %d); a step id spent on a stream that "+
			"does not exist is a gap in every later block id", wantSegments, p.nextSegment)
	}
}

// TestRollingBackTheSameBatchTwiceDoesNotUnderflowTheCounters pins the
// zero-count half of the same guards.
//
// appendLocked hands the dropped batch back on failure, and a retry path that
// re-reported the same batch would undo it twice. The counters are signed
// (fragments) and drive INV-1 directly, so an unguarded second decrement makes
// the aggregate claim a NEGATIVE fragment count - which is neither the
// "nothing shipped" case nor the "something shipped" case, and no consumer
// handles it.
func TestRollingBackTheSameBatchTwiceDoesNotUnderflowTheCounters(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	answer := "the answer nobody stored"

	lost := p.Project(rootEvent(events.KindAssistant, answer, "delta"))
	lost = append(lost, p.Project(rootEvent(events.KindThinking, "some reasoning", "delta"))...)
	if len(lost) != 2 {
		t.Fatalf("the seeded batch is %d wire events, want 2:\n%s", len(lost), describeWire(lost))
	}

	p.RollbackStreaming(lost)
	p.RollbackStreaming(lost)

	ts := p.turns["turn:1"]
	if ts == nil {
		t.Fatal("the turn state vanished")
	}
	if ts.fragments != 0 || ts.thinkingFragments != 0 {
		t.Fatalf("after two undos fragments=%d thinkingFragments=%d, want 0 and 0; a second decrement "+
			"drives them negative and the aggregate then claims fragments the viewer cannot have",
			ts.fragments, ts.thinkingFragments)
	}

	payload := settledMessage(t, p)
	if payload.Fragments != 0 || payload.Text != "the second answer" {
		t.Errorf("Fragments = %d, Text = %q; want 0 and the whole answer - nothing shipped, so the "+
			"aggregate is the only copy", payload.Fragments, payload.Text)
	}
}

// TestALostDropMarkerIsReportedByTheNextOne is RollbackDrops' partial arm.
//
// checkDrops moves the watermark when it CONSTRUCTS the marker. When the
// append that would have made it durable fails, the marker never reaches the
// wire while the watermark has already moved, so the loss it reported becomes
// invisible - the next marker under-reports by exactly that amount and the
// transcript claims fewer missing events than it has. The undo has to put the
// watermark back to what was STORED, not to zero: an earlier marker that DID
// reach the wire has already accounted for its own share.
func TestALostDropMarkerIsReportedByTheNextOne(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	first := p.ProjectWithDrops(rootEvent(events.KindAssistant, "one", "delta"), 5)
	stored := droppedPayload(t, first)
	if stored.Dropped != 5 || stored.TotalDropped != 5 {
		t.Fatalf("the first marker reported %d of %d, want 5 of 5", stored.Dropped, stored.TotalDropped)
	}

	// Three more events are shed and the marker that reports them is lost.
	lost := p.ProjectWithDrops(rootEvent(events.KindAssistant, "two", "delta"), 8)
	losing := droppedPayload(t, lost)
	if losing.Dropped != 3 {
		t.Fatalf("the second marker reported %d, want 3", losing.Dropped)
	}
	p.RollbackDrops(losing.Dropped)

	// Nothing new was shed. The next projection must still report the three.
	repair := p.ProjectWithDrops(rootEvent(events.KindAssistant, "three", "delta"), 8)
	repaired := droppedPayload(t, repair)
	if repaired.Dropped != 3 {
		t.Errorf("the repair marker reported %d dropped, want 3 - the marker that reported them was "+
			"never stored, so this is the transcript's only statement of that hole", repaired.Dropped)
	}
	if repaired.TotalDropped != 8 {
		t.Errorf("the repair marker's TotalDropped = %d, want 8 - the running total is what a reader "+
			"uses to tell a repeat from a new loss", repaired.TotalDropped)
	}
	// The five the FIRST marker accounted for must not be re-reported: the
	// undo walks the watermark back by the lost delta, not to zero.
	if repaired.Dropped == 8 {
		t.Error("the repair marker re-reported the whole running total; the first marker is on the wire " +
			"and its five events are already accounted for")
	}
}

// droppedPayload extracts the sync.dropped marker from a projected batch.
func droppedPayload(t *testing.T, evs []WireEvent) *SyncDroppedPayload {
	t.Helper()
	for _, we := range evs {
		if payload, ok := we.Payload.(*SyncDroppedPayload); ok {
			return payload
		}
	}
	t.Fatalf("no sync.dropped marker in the batch:\n%s", describeWire(evs))
	return nil
}
