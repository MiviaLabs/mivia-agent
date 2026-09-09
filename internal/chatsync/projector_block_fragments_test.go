package chatsync

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// `assistant.message.fragments` is a per-BLOCK count on a per-block event.
//
// The aggregate names one block - the segment its surviving deltas shipped
// into - and reported ts.fragments, which counts every delta of the TURN. On a
// turn with prose on both sides of a tool call the last block claimed the
// first block's deltas too. Thinking got this right from the start
// (thinkingBlockFragments); these tests hold the assistant path to the same
// rule on both the root and the lane paths.

// TestTheAssistantAggregateCountsOnlyTheBlockItNames is the decisive case.
func TestTheAssistantAggregateCountsOnlyTheBlockItNames(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindAssistant, "one ", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(toolEvent(events.KindToolEnd, "call-1"))
	p.Project(rootEvent(events.KindAssistant, "two ", "delta"))

	payload := settledMessage(t, p)
	if payload.Block != "turn:1:assistant:1" {
		t.Fatalf("settled block = %q, want turn:1:assistant:1", payload.Block)
	}
	if payload.Fragments != 1 {
		t.Errorf("Fragments = %d, want 1 - block :1 holds one delta (\"two \"); "+
			"the turn-wide count claims block :0's delta for it", payload.Fragments)
	}
}

// TestAFlushedTailIsTheOnlyFragmentOfItsBlock is the reported shape: a short
// second message held whole by the redactor ships nothing until the aggregate
// flushes it as the block's ONE delta - yet the aggregate said two. The delta's
// turn-wide index is unchanged; only the block count moves.
func TestAFlushedTailIsTheOnlyFragmentOfItsBlock(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, "before ", "delta"))...)
	// The first delta must actually ship for this shape; the hold-back
	// releases it on the tool start if not before.
	out = append(out, p.Project(toolEvent(events.KindToolStart, "call-1"))...)
	out = append(out, p.Project(toolEvent(events.KindToolEnd, "call-1"))...)
	if shippedText(out) != "before " {
		t.Fatalf("block :0's delta did not ship: %q", shippedText(out))
	}
	held := p.Project(rootEvent(events.KindAssistant, "held", "delta"))
	if len(held) != 0 {
		t.Fatalf("the short delta shipped %d events; it must be held whole", len(held))
	}
	out = p.Project(rootEvent(events.KindAssistant, "held", ""))
	if len(out) != 2 {
		t.Fatalf("aggregate produced %d events, want 2 - the flushed tail then the message", len(out))
	}
	tail, ok := out[0].Payload.(*AssistantDeltaPayload)
	if !ok {
		t.Fatalf("first event is %T, want *AssistantDeltaPayload", out[0].Payload)
	}
	if tail.Index != 1 {
		t.Errorf("tail Index = %d, want 1 - the delta index stays turn-wide", tail.Index)
	}
	msg, ok := out[1].Payload.(*AssistantMessagePayload)
	if !ok {
		t.Fatalf("second event is %T, want *AssistantMessagePayload", out[1].Payload)
	}
	if msg.Block != tail.Block {
		t.Fatalf("message block %q differs from the tail's %q", msg.Block, tail.Block)
	}
	if msg.Fragments != 1 {
		t.Errorf("Fragments = %d, want 1 - the flushed tail is this block's only delta", msg.Fragments)
	}
}

// TestARolledBackDeltaLeavesTheBlockCountHonest: a lost delta comes off the
// block it was counted into, and the block count never inherits the previous
// block's deltas through the rollback.
func TestARolledBackDeltaLeavesTheBlockCountHonest(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindAssistant, "one ", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(toolEvent(events.KindToolEnd, "call-1"))
	p.Project(rootEvent(events.KindAssistant, "two ", "delta"))
	lost := p.Project(rootEvent(events.KindAssistant, "three", "delta"))
	p.RollbackStreaming(lost)

	payload := settledMessage(t, p)
	if payload.Block != "turn:1:assistant:1" {
		t.Fatalf("settled block = %q, want turn:1:assistant:1", payload.Block)
	}
	if payload.Fragments != 1 {
		t.Errorf("Fragments = %d, want 1 - block :1 holds \"two \" only after the "+
			"rollback; \"one \" is block :0's", payload.Fragments)
	}
}

// TestASubagentAggregateCountsOnlyItsOwnBlock is the lane twin: a subagent's
// tool start closes the LANE's block, and the lane's aggregate must count per
// block exactly as the root's does.
func TestASubagentAggregateCountsOnlyItsOwnBlock(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(subagentEvent(events.KindAssistant, "task-1", "one ", "delta"))
	p.Project(subagentEvent(events.KindSubagentStart, "task-1", "", ""))
	p.Project(subagentEvent(events.KindSubagentEnd, "task-1", "", "completed"))
	p.Project(subagentEvent(events.KindAssistant, "task-1", "two ", "delta"))

	out := p.Project(subagentEvent(events.KindAssistant, "task-1", "one two ", ""))
	if len(out) != 1 {
		t.Fatalf("lane aggregate produced %d events, want 1", len(out))
	}
	payload, ok := out[0].Payload.(*SubagentAssistantMessagePayload)
	if !ok {
		t.Fatalf("got %T, want *SubagentAssistantMessagePayload", out[0].Payload)
	}
	if payload.Fragments != 1 {
		t.Errorf("Fragments = %d, want 1 - the lane's block :1 holds one delta", payload.Fragments)
	}
	if payload.Text != "" {
		t.Errorf("Text = %q, want empty - INV-1, a delta shipped into this block", payload.Text)
	}
}

// TestARollbackThatEmptiesABlockRestoresThePreviousCount is a GUARD, not a
// discriminator: it passes with the turn-wide count too (3 - 1 = 2). It exists
// because a naive `blockFragments = 0` on the fall-back would report 0 and
// re-ship block :0's text on top of two deltas already on the wire.
func TestARollbackThatEmptiesABlockRestoresThePreviousCount(t *testing.T) {
	p := NewProjector("sess-1", 0, proseOpts())

	p.Project(rootEvent(events.KindAssistant, "one ", "delta"))
	p.Project(rootEvent(events.KindAssistant, "two ", "delta"))
	p.Project(toolEvent(events.KindToolStart, "call-1"))
	p.Project(toolEvent(events.KindToolEnd, "call-1"))
	lost := p.Project(rootEvent(events.KindAssistant, "three", "delta"))
	p.RollbackStreaming(lost)

	payload := settledMessage(t, p)
	if payload.Block != "turn:1:assistant:0" {
		t.Fatalf("settled block = %q, want turn:1:assistant:0 - the block the "+
			"surviving deltas used", payload.Block)
	}
	if payload.Fragments != 2 {
		t.Errorf("Fragments = %d, want 2 - both of block :0's deltas shipped", payload.Fragments)
	}
	if payload.Text != "" {
		t.Errorf("Text = %q, want empty - INV-1; a full text here doubles the prose", payload.Text)
	}
}
