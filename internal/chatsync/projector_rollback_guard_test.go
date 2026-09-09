package chatsync

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Discriminators for two guards in the assistant rollback path that the rest
// of the suite does not distinguish from their predecessors.
//
// The first is rollbackOneDelta's fall-back guard, `blockFragments == 0`. It
// replaced `segmentAssistant == 0`, and the two agree on every batch that is
// rolled back before the step moves on. They part exactly where
// flushHeldAssistantOnStepClose puts them: a tool start flushes the held tail
// as a delta AND advances the step in the same Project call, so the batch
// handed back to RollbackStreaming is [tail delta, tool.started] and
// segmentAssistant is already zero when the delta is undone. The step counter
// then says "this block is empty" about a block that still holds every delta
// that shipped before the tail.

// TestARolledBackTailAcrossAToolStartKeepsItsBlock: block :0 has one delta,
// block :1 has two long deltas on the wire and a held tail; the tool start
// that flushes the tail is lost. The settle must stay on block :1 with its
// two surviving deltas. Falling back names block :0 for a count of 1, which
// files block :1's two stored deltas under a block that holds one.
func TestARolledBackTailAcrossAToolStartKeepsItsBlock(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())
	long := strings.Repeat("a", 300) + " "

	// Block :0: one short delta, held whole, flushed by the first tool start
	// as the block's one delta.
	if out := p.Project(rootEvent(events.KindAssistant, "one ", "delta")); len(out) != 0 {
		t.Fatalf("the short delta shipped %d events; it must be held whole", len(out))
	}
	out := p.Project(toolEvent(events.KindToolStart, "call-1"))
	if shippedText(out) != "one " {
		t.Fatalf("block :0's delta did not flush on the tool start: %q", shippedText(out))
	}
	p.Project(toolEvent(events.KindToolEnd, "call-1"))

	// Block :1: two deltas long enough to ship past the hold-back.
	for i, text := range []string{long, long} {
		if got := p.Project(rootEvent(events.KindAssistant, text, "delta")); len(got) != 1 {
			t.Fatalf("block :1 delta %d shipped %d events, want 1", i, len(got))
		}
	}

	// The second tool start flushes block :1's held tail and advances the
	// step in one call; that whole batch is lost.
	lost := p.Project(toolEvent(events.KindToolStart, "call-2"))
	if len(lost) != 2 {
		t.Fatalf("tool start produced %d events, want 2 - the flushed tail then tool.started", len(lost))
	}
	if _, ok := lost[0].Payload.(*AssistantDeltaPayload); !ok {
		t.Fatalf("first event is %T, want *AssistantDeltaPayload", lost[0].Payload)
	}
	p.RollbackStreaming(lost)

	payload := settledMessage(t, p)
	if payload.Block != "turn:1:assistant:1" {
		t.Fatalf("settled block = %q, want turn:1:assistant:1 - two of its deltas "+
			"are still on the wire; the step counter was zeroed by the same tool "+
			"start whose tail was lost and says nothing about them", payload.Block)
	}
	if payload.Fragments != 2 {
		t.Errorf("Fragments = %d, want 2 - block :1's two shipped deltas survive the rollback", payload.Fragments)
	}
	if payload.Text != "" {
		t.Errorf("Text = %q, want empty - INV-1; two deltas shipped into this block", payload.Text)
	}
}

// The second guard is the `blockFragments > 0` term on both aggregates. The
// commit that added it names the corner it covers as "two consecutive blocks
// both emptied by rollback". Through Project and RollbackStreaming that corner
// does not arise: the fall-back re-arms after every undo, because it restores
// streamSegment to prevStreamSegment and the next block's recording retires
// that pair again (an exhaustive walk of every sequence of up to eight
// long/short/thinking/reset/tool events, each optionally rolled back, reaches
// no settle with `streamed && blockFragments == 0`). The term is therefore a
// runtime guard on state the producer does not reach today, and the two tests
// after the next seed that state directly: a bookkeeping slip anywhere in the
// counters must fall on the full-text side, never on `text: ""` with
// `fragments: 0`, which loses the answer outright.

// TestTwoEmptiedBlocksStillSettleOnTheSurvivingOne is the corner as named, a
// GUARD rather than a discriminator: it passes without the term too, and pins
// that the one-deep undo does repair it. Block :0 keeps its delta; blocks :1
// and :2 each lose their only delta; the settle names block :0 with its count.
func TestTwoEmptiedBlocksStillSettleOnTheSurvivingOne(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindAssistant, "one ", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(toolEvent(events.KindToolEnd, "call-1"))
	p.RollbackStreaming(p.Project(rootEvent(events.KindAssistant, "two ", "delta")))
	// The lost delta spent nothing, so the step only moves on if the model
	// reasons before it acts again; reasoning gives block :2 its own id.
	p.Project(rootEvent(events.KindThinking, "hmm", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-2"))
	p.Project(toolEvent(events.KindToolEnd, "call-2"))
	p.RollbackStreaming(p.Project(rootEvent(events.KindAssistant, "three", "delta")))

	payload := settledMessage(t, p)
	if payload.Block != "turn:1:assistant:0" {
		t.Fatalf("settled block = %q, want turn:1:assistant:0 - the one block whose delta survived", payload.Block)
	}
	if payload.Fragments != 1 || payload.Text != "" {
		t.Errorf("Fragments = %d, Text = %q; want 1 and empty - block :0's delta is on the wire",
			payload.Fragments, payload.Text)
	}
}

// TestTheAggregateNeverEmptiesTextOnAZeroBlockCount: a streamed turn whose
// named block reports no delta must carry the full text. `text: ""` with
// `fragments: 0` satisfies INV-1's letter and gives the viewer nothing.
func TestTheAggregateNeverEmptiesTextOnAZeroBlockCount(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	p.Project(rootEvent(events.KindAssistant, "one ", "delta"))
	ts := p.turns["turn:1"]
	if ts == nil || !ts.streamed || ts.blockFragments != 1 {
		t.Fatalf("precondition: turn state %+v is not a streamed turn with one block delta", ts)
	}
	// The state the guard exists for, seeded directly: see the note above.
	ts.blockFragments = 0

	payload := settledMessage(t, p)
	if payload.Fragments != 0 {
		t.Fatalf("Fragments = %d, want 0 - the named block holds no counted delta", payload.Fragments)
	}
	if payload.Text != "the second answer" {
		t.Errorf("Text = %q, want the whole answer - with no fragments to claim, "+
			"an empty text is the reply lost outright", payload.Text)
	}
}

// TestTheLaneAggregateNeverEmptiesTextOnAZeroBlockCount is the lane twin.
func TestTheLaneAggregateNeverEmptiesTextOnAZeroBlockCount(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())
	p.Project(subagentEvent(events.KindAssistant, "task-1", "one ", "delta"))
	ls := p.lanes["turn:1\x00task-1"]
	if ls == nil || !ls.streamed || ls.blockFragments != 1 {
		t.Fatalf("precondition: lane state %+v is not a streamed lane with one block delta", ls)
	}
	ls.blockFragments = 0

	out := p.Project(subagentEvent(events.KindAssistant, "task-1", "one two", ""))
	if len(out) != 1 {
		t.Fatalf("lane aggregate produced %d events, want 1", len(out))
	}
	payload, ok := out[0].Payload.(*SubagentAssistantMessagePayload)
	if !ok {
		t.Fatalf("got %T, want *SubagentAssistantMessagePayload", out[0].Payload)
	}
	if payload.Fragments != 0 {
		t.Fatalf("Fragments = %d, want 0 - the lane's named block holds no counted delta", payload.Fragments)
	}
	if payload.Text != "one two" {
		t.Errorf("Text = %q, want the whole answer - the lane path takes the same safe side as the root's", payload.Text)
	}
}
