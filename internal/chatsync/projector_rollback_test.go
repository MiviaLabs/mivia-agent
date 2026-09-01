package chatsync

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// A wire event can be projected and then never stored: the outbox overflows on
// a slow or offline uplink, and appendLocked hands what it dropped back to
// RollbackStreaming. The producer's counters must then describe what the
// VIEWER holds, not what this side intended to send.
//
// These tests drive that path. The previous rollback for a reset wrote back
// exactly the values the reset had set - streamed=false, fragments=0 - which
// is not a rollback but a restatement, and nothing failed because nothing
// tested it.

// settledMessage runs the turn's aggregate and returns it.
func settledMessage(t *testing.T, p *Projector) *AssistantMessagePayload {
	t.Helper()
	got := p.Project(rootEvent(events.KindAssistant, "the second answer", ""))
	if len(got) != 1 {
		t.Fatalf("the settled aggregate produced %d wire events, want 1", len(got))
	}
	payload, ok := got[0].Payload.(*AssistantMessagePayload)
	if !ok {
		t.Fatalf("settled payload is %T, want *AssistantMessagePayload", got[0].Payload)
	}
	return payload
}

// TestALostResetMakesTheSettledMessageCarryTheAnswer is the defect.
//
// The viewer holds the abandoned attempt's fragments and never heard the
// discard. This side cannot say how many fragments that is - the reset zeroed
// the count before the append failed - so the only repair it can offer is the
// full text, which a viewer writes over its stitched text. Reporting a count
// that covers the retry alone makes INV-1 empty the text, and the viewer keeps
// two attempts welded together with nothing to replace them.
func TestALostResetMakesTheSettledMessageCarryTheAnswer(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindAssistant, "the first ", "delta"))
	p.Project(rootEvent(events.KindAssistant, "answer", "delta"))

	reset := p.Project(rootEvent(events.KindAssistantReset, "", "schema_retry"))
	if len(reset) != 1 {
		t.Fatalf("the reset produced %d wire events, want 1", len(reset))
	}
	// The append lost it: it was projected, and never stored.
	p.RollbackStreaming(reset)

	// The retry streams its own answer, which is the case the previous
	// rollback got wrong - it reported a count covering only this attempt.
	p.Project(rootEvent(events.KindAssistant, "the second answer", "delta"))

	payload := settledMessage(t, p)
	if payload.Text != "the second answer" {
		t.Errorf("Text = %q, want the whole answer: the viewer still holds the "+
			"abandoned attempt and this message is the only thing that can "+
			"replace it", payload.Text)
	}
	if payload.Fragments != 0 {
		t.Errorf("Fragments = %d, want 0 - a non-zero count empties the text "+
			"under INV-1 and counts only the attempt the viewer did not lose",
			payload.Fragments)
	}
}

// TestAStoredResetStillReportsFragments is the other half. Nothing is broken
// when the reset DID reach the wire, so the ordinary streamed accounting must
// survive: a viewer that got the discard has already cleared the attempt, and
// re-sending the whole answer beside its own deltas is the duplicate the
// fragment count exists to prevent.
func TestAStoredResetStillReportsFragments(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindAssistant, "the first answer", "delta"))
	p.Project(rootEvent(events.KindAssistantReset, "", "schema_retry"))
	p.Project(rootEvent(events.KindAssistant, "the second answer", "delta"))

	payload := settledMessage(t, p)
	if payload.Fragments != 1 || payload.Text != "" {
		t.Errorf("Fragments = %d Text = %q, want 1 and empty: the viewer got the "+
			"discard and holds exactly this attempt's one delta",
			payload.Fragments, payload.Text)
	}
}

// TestAFinishedTurnRetiresItsLanes covers the reclamation gap.
//
// retireLane fires on one run's terminal, and both hops that carry a terminal
// are bounded drop-oldest queues that shed under load. A run whose terminal
// was shed used to sit in the lane table until 64 later keys pushed it out, so
// the LRU was the reclamation policy and the entry it evicted could be a run
// that was still streaming.
func TestAFinishedTurnRetiresItsLanes(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(subagentEvent(events.KindAssistant, "task-1", "sub text", "delta"))
	if len(p.lanes) != 1 {
		t.Fatalf("the lane was never created; this test proves nothing")
	}

	// The run's terminal is shed by the bus - it never arrives.
	p.Project(rootEvent(events.KindTurnEnd, "", "completed"))

	if len(p.lanes) != 0 {
		t.Errorf("%d lane(s) survived the turn that owned them; nothing but the "+
			"LRU will ever reclaim them, and the entry it evicts can be a live "+
			"run", len(p.lanes))
	}
	if len(p.laneOrder) != 0 {
		t.Errorf("laneOrder still holds %d key(s) with no lane behind them",
			len(p.laneOrder))
	}
}
