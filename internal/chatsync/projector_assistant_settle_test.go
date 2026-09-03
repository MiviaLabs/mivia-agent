package chatsync

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// The per-block assistant settle.
//
// A consumer marks a prose block complete on its aggregate and nothing else
// (apps/web/src/lib/chat-sync/grouping.ts sets isCompleted on assistant-message
// only). finalizeSDKTurn publishes ONE aggregate per TURN, so a message the
// model had finished - and the loop had flagged complete - stayed "streaming"
// in the viewer through every reasoning pass and tool call that followed it,
// until the turn's end. Real wire data: 484 deltas over 9.8s, then nothing on
// that block for the rest of the turn. Thinking never had this defect, because
// thinking.message settles PER BLOCK; these tests pin the assistant twin.

// assistantAggregates returns every root aggregate in evs, in order.
func assistantAggregates(evs []WireEvent) []*AssistantMessagePayload {
	var out []*AssistantMessagePayload
	for _, we := range evs {
		if m, ok := we.Payload.(*AssistantMessagePayload); ok {
			out = append(out, m)
		}
	}
	return out
}

// deltaCountByBlock counts the assistant deltas (root and lane) per block id.
func deltaCountByBlock(evs []WireEvent) map[string]int {
	counts := map[string]int{}
	for _, we := range evs {
		switch payload := we.Payload.(type) {
		case *AssistantDeltaPayload:
			counts[payload.Block]++
		case *SubagentAssistantDeltaPayload:
			counts[payload.Block]++
		}
	}
	return counts
}

// TestACompletedMessageSettlesItsBlockBeforeTheTurnEnds is the decisive
// test. Deltas ship for a block, the loop flags the message complete, and NO
// tool call and NO turn end follow: the block's own aggregate must already be
// on the wire, naming that block, with INV-1 (fragments = the block's shipped
// deltas, text empty).
func TestACompletedMessageSettlesItsBlockBeforeTheTurnEnds(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := strings.Repeat("the finished answer runs on for a while. ", 10) +
		"and this is its final clause."

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, whole, "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))...)

	aggs := assistantAggregates(out)
	if len(aggs) != 1 {
		t.Fatalf("the completion flag settled %d aggregates, want exactly 1: the "+
			"viewer has no other signal that this message is finished", len(aggs))
	}
	agg := aggs[0]
	counts := deltaCountByBlock(out)
	if len(counts) != 1 {
		t.Fatalf("deltas landed on %d blocks, want 1: %v", len(counts), counts)
	}
	var block string
	for b := range counts {
		block = b
	}
	if agg.Block != block {
		t.Fatalf("the settle names block %q; the deltas shipped into %q", agg.Block, block)
	}
	if agg.Fragments != counts[block] || agg.Text != "" {
		t.Fatalf("INV-1: Fragments = %d Text = %q, want %d and empty", agg.Fragments, agg.Text, counts[block])
	}
	if agg.Status != "completed" {
		t.Fatalf("Status = %q, want completed", agg.Status)
	}
	if shippedText(out) != whole {
		t.Fatalf("the settle did not ship the whole text first:\ngot  %q\nwant %q", shippedText(out), whole)
	}
	// Order: the held tail is a delta and travels BEFORE the aggregate that
	// empties its own text under INV-1.
	last := out[len(out)-1]
	if _, ok := last.Payload.(*AssistantMessagePayload); !ok {
		t.Fatalf("the aggregate must be the last event of the settle, got %T", last.Payload)
	}
}

// TestACompletedLaneMessageSettlesItsOwnBlock is the subagent twin: the flag
// reaches the projector with the lane's attribution, so the settle is the
// LANE's aggregate on the lane's block, never a root one.
func TestACompletedLaneMessageSettlesItsOwnBlock(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := strings.Repeat("the subagent's finished answer runs on. ", 10) +
		"and this is its final clause."

	var out []WireEvent
	out = append(out, p.Project(subagentEvent(events.KindAssistant, "task-1", whole, "delta"))...)
	out = append(out, p.Project(subagentEvent(events.KindAssistant, "task-1", "", events.DetailAssistantComplete))...)

	var lane *SubagentAssistantMessagePayload
	for _, we := range out {
		switch m := we.Payload.(type) {
		case *AssistantMessagePayload:
			t.Fatalf("a lane's completion settled a ROOT aggregate on %q", m.Block)
		case *SubagentAssistantMessagePayload:
			if lane != nil {
				t.Fatal("the lane settled twice")
			}
			lane = m
		}
	}
	if lane == nil {
		t.Fatal("the lane's completion flag settled nothing; the lane's message stays live until its run ends")
	}
	if !strings.HasPrefix(lane.Block, "turn:1:task-1:assistant:") {
		t.Errorf("settled block = %q, want the lane's block turn:1:task-1:assistant:<seg>", lane.Block)
	}
	counts := deltaCountByBlock(out)
	if lane.Fragments != counts[lane.Block] || lane.Text != "" {
		t.Fatalf("INV-1: Fragments = %d Text = %q, want %d and empty", lane.Fragments, lane.Text, counts[lane.Block])
	}
}

// TestAMessageCompleteWithoutDeltasLeavesTheTurnAggregateAlone pins the rule
// that makes a per-block settle admissible at all: with stream_assistant off
// no delta ships, the flag has nothing to settle, and the turn-end aggregate
// must stay the SOLE carrier of the text. A settle here would be an empty
// message with a zero count, or the answer twice.
func TestAMessageCompleteWithoutDeltasLeavesTheTurnAggregateAlone(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{StreamAssistant: false})

	whole := "the whole answer, never streamed"
	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, whole, "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))...)
	if len(out) != 0 {
		t.Fatalf("with no delta on the wire the flag produced %d events: %+v", len(out), out)
	}
	out = p.Project(rootEvent(events.KindAssistant, whole, ""))
	aggs := assistantAggregates(out)
	if len(aggs) != 1 {
		t.Fatalf("turn-end aggregate: got %d aggregates, want 1", len(aggs))
	}
	if aggs[0].Fragments != 0 || aggs[0].Text != whole {
		t.Fatalf("Fragments = %d Text = %q, want 0 and the whole text", aggs[0].Fragments, aggs[0].Text)
	}
}

// TestTwoSettledBlocksThenTheTurnEndDeliverEachTextOnce drives a turn with
// prose on both sides of a tool call, each message flagged complete, then the
// turn's own aggregate and end. No block may deliver its text twice, none may
// lose it, and INV-1 must hold on every aggregate of every block.
func TestTwoSettledBlocksThenTheTurnEndDeliverEachTextOnce(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	first := strings.Repeat("Let me look at the config first, carefully. ", 10)
	second := strings.Repeat("Here is what the configuration actually says. ", 12)

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, first, "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))...)
	out = append(out, p.Project(toolEvent(events.KindToolStart, "call-1"))...)
	out = append(out, p.Project(toolEvent(events.KindToolEnd, "call-1"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, second, "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))...)
	// finalizeSDKTurn's terminal aggregate carries the FINAL message only.
	out = append(out, p.Project(rootEvent(events.KindAssistant, second, ""))...)
	out = append(out, p.Project(rootEvent(events.KindTurnEnd, "", "completed"))...)

	deltas, aggregates := proseByBlock(out)
	if len(deltas) != 2 {
		t.Fatalf("deltas landed on %d blocks, want 2: %v", len(deltas), deltas)
	}
	want := map[string]bool{first: false, second: false}
	for block, text := range deltas {
		if _, ok := want[text]; !ok {
			t.Fatalf("block %s carries %q, which is neither message whole", block, text)
		}
		want[text] = true
		if aggregates[block] != "" {
			t.Fatalf("block %s ships deltas AND aggregate text %q - the reader sees it twice", block, aggregates[block])
		}
	}
	for text, seen := range want {
		if !seen {
			t.Fatalf("no block carries %q whole - the text was lost", text)
		}
	}
	counts := deltaCountByBlock(out)
	settled := map[string]bool{}
	for _, agg := range assistantAggregates(out) {
		if agg.Fragments != counts[agg.Block] || agg.Text != "" {
			t.Fatalf("INV-1 on %s: Fragments = %d Text = %q, want %d and empty", agg.Block, agg.Fragments, agg.Text, counts[agg.Block])
		}
		settled[agg.Block] = true
	}
	for block := range deltas {
		if !settled[block] {
			t.Fatalf("block %s streamed and was never settled", block)
		}
	}
}

// TestAFlagWithNoNewDeltasDoesNotResettle: the loop flags EVERY completed
// message, including one that only called tools and said nothing. Such a
// flag must not settle the previous block again - the settle is per block,
// not per iteration.
func TestAFlagWithNoNewDeltasDoesNotResettle(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := strings.Repeat("the finished answer runs on for a while. ", 10)
	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, whole, "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))...)
	if n := len(assistantAggregates(out)); n != 1 {
		t.Fatalf("first flag settled %d aggregates, want 1", n)
	}
	out = append(out, p.Project(toolEvent(events.KindToolStart, "call-1"))...)
	out = append(out, p.Project(toolEvent(events.KindToolEnd, "call-1"))...)
	again := p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))
	if n := len(assistantAggregates(again)); n != 0 {
		t.Fatalf("a flag with no new deltas settled %d aggregates, want 0", n)
	}
}

// TestARolledBackSettleIsRetriedOnTheNextFlag: the settle tracks what was
// STORED, like every other counter here. A settle whose append failed must
// not leave the block marked settled, or no later flag would ever retry it.
func TestARolledBackSettleIsRetriedOnTheNextFlag(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := strings.Repeat("the finished answer runs on for a while. ", 10)
	p.Project(rootEvent(events.KindAssistant, whole, "delta"))
	settle := p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))
	aggs := assistantAggregates(settle)
	if len(aggs) != 1 {
		t.Fatalf("flag settled %d aggregates, want 1", len(aggs))
	}
	// Only the aggregate failed to store; the tail delta before it did.
	p.RollbackStreaming(settle[len(settle)-1:])

	retry := assistantAggregates(p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete)))
	if len(retry) != 1 {
		t.Fatalf("after the settle was rolled back the next flag settled %d aggregates, want 1", len(retry))
	}
	if retry[0].Block != aggs[0].Block || retry[0].Fragments != aggs[0].Fragments || retry[0].Text != "" {
		t.Fatalf("retry = %+v, want the same block and count as %+v", retry[0], aggs[0])
	}
}

// TestEachFlaggedBlockSettlesBeforeTheTurnEndsNotOnlyTheFirst is the
// discriminator for the second block. TestTwoSettledBlocksThenTheTurnEnd...
// accepts the turn-end aggregate as block :1's settle, so a projector that
// settles only the FIRST flagged block (assistantSettled never cleared by a
// later delta) passes it - and every block after the first regresses to
// settling at turn end, the exact defect. This test cuts the turn-end
// aggregate out: BOTH settles must be on the wire, naming distinct blocks
// in delta order, before the terminal EventAssistant is projected.
func TestEachFlaggedBlockSettlesBeforeTheTurnEndsNotOnlyTheFirst(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	first := strings.Repeat("Let me look at the config first, carefully. ", 10)
	second := strings.Repeat("Here is what the configuration actually says. ", 12)

	var before []WireEvent
	before = append(before, p.Project(rootEvent(events.KindAssistant, first, "delta"))...)
	before = append(before, p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))...)
	before = append(before, p.Project(toolEvent(events.KindToolStart, "call-1"))...)
	before = append(before, p.Project(toolEvent(events.KindToolEnd, "call-1"))...)
	before = append(before, p.Project(rootEvent(events.KindAssistant, second, "delta"))...)
	before = append(before, p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))...)

	deltas, _ := proseByBlock(before)
	var firstBlock, secondBlock string
	for block, text := range deltas {
		switch text {
		case first:
			firstBlock = block
		case second:
			secondBlock = block
		}
	}
	if firstBlock == "" || secondBlock == "" || firstBlock == secondBlock {
		t.Fatalf("deltas did not land on two distinct blocks: %v", deltas)
	}

	aggs := assistantAggregates(before)
	if len(aggs) != 2 {
		t.Fatalf("before the turn-end aggregate %d settles are on the wire, want 2 "+
			"(one per flagged block); the second block would sit streaming until turn end", len(aggs))
	}
	if aggs[0].Block != firstBlock || aggs[1].Block != secondBlock {
		t.Fatalf("settles name %q then %q, want %q then %q", aggs[0].Block, aggs[1].Block, firstBlock, secondBlock)
	}
	counts := deltaCountByBlock(before)
	for _, agg := range aggs {
		if agg.Fragments != counts[agg.Block] || agg.Text != "" {
			t.Fatalf("INV-1 on %s: Fragments = %d Text = %q, want %d and empty", agg.Block, agg.Fragments, agg.Text, counts[agg.Block])
		}
	}

	// The turn-end aggregate still follows, as the backstop, and adds
	// nothing new: it names the last block with an empty text.
	after := p.Project(rootEvent(events.KindAssistant, second, ""))
	tail := assistantAggregates(after)
	if len(tail) != 1 || tail[0].Block != secondBlock || tail[0].Text != "" {
		t.Fatalf("turn-end aggregate: got %d, want 1 naming %q with empty text", len(tail), secondBlock)
	}
}

// TestASettleAfterAFallbackReportsThePreviousBlockBytes pins the byte
// counter to the same one-deep undo the fragment counter has. When a
// rollback empties the named block and falls back to the previous one, the
// settle that follows must report THAT block's shipped bytes, not zero.
func TestASettleAfterAFallbackReportsThePreviousBlockBytes(t *testing.T) {
	ts := &turnState{}
	ts.streamed = true
	ts.recordDeltaSegment(1, 40)
	ts.recordDeltaSegment(1, 25)
	ts.fragments = 2
	// A delta opens segment 2, then its append fails.
	ts.recordDeltaSegment(2, 9)
	ts.fragments = 3
	p := &Projector{}
	p.rollbackOneDelta(ts)
	if ts.streamSegment != 1 || ts.blockFragments != 2 {
		t.Fatalf("fallback: segment %d fragments %d, want 1 and 2", ts.streamSegment, ts.blockFragments)
	}
	if ts.blockBytes != 65 {
		t.Fatalf("blockBytes after fallback = %d, want 65 (the previous block's shipped bytes)", ts.blockBytes)
	}
}
