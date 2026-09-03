package chatsync

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// The hold-back's two failure modes, stated at the projector.
//
// A streaming redactor withholds a bounded tail so a secret split across two
// deltas is still caught. That trade is only sound while the tail is (a) never
// dropped and (b) released promptly. e6153845 shipped it with both properties
// broken for a MID-TURN prose block:
//
//   - The drop was justified by "the turn's own aggregate still carries the
//     whole text". It does not. finalizeSDKTurn (internal/agent/agentloop_run.go)
//     publishes ONE terminal EventAssistant per turn carrying res.Final.Content
//     - the LAST message only - and the projector files it under the ONE
//     segment its deltas used. No aggregate ever names an earlier block, so a
//     first prose block shorter than redact.StreamHoldBack was deleted outright.
//   - The release waited for the next block CLOSE (a tool start, the turn's
//     end). A message the model has finished saying therefore sat 256 bytes
//     short in the viewer for as long as the gap lasted - a stop hook, a long
//     reasoning pass - while the local TUI, which streams the same deltas with
//     no hold-back at all, already showed it whole.

// proseByBlock returns, per prose block id, the text its assistant deltas
// carried and the text its settled aggregate carried. Everything the reader
// can possibly reconstruct is in here, keyed exactly as the consumer keys it
// (apps/web/src/lib/chat-sync/grouping.ts).
func proseByBlock(evs []WireEvent) (deltas, aggregates map[string]string) {
	deltas, aggregates = map[string]string{}, map[string]string{}
	for _, we := range evs {
		switch payload := we.Payload.(type) {
		case *AssistantDeltaPayload:
			deltas[payload.Block] += payload.Text
		case *AssistantMessagePayload:
			aggregates[payload.Block] += payload.Text
		case *SubagentAssistantDeltaPayload:
			deltas[payload.Block] += payload.Text
		case *SubagentAssistantMessagePayload:
			aggregates[payload.Block] += payload.Text
		}
	}
	return deltas, aggregates
}

// firstShortBlockTurn drives the reported production shape: a SHORT first
// prose block (entirely inside the hold-back window, so no delta ever ships),
// a tool call, then a longer second block that streams normally and is the one
// the turn's single aggregate names.
func firstShortBlockTurn(t *testing.T, p *Projector) (out []WireEvent, first, second string) {
	t.Helper()
	first = "Let me look at the config first."
	second = strings.Repeat("Here is what the configuration actually says. ", 12)

	out = append(out, p.Project(rootEvent(events.KindAssistant, first, "delta"))...)
	out = append(out, p.Project(toolEvent(events.KindToolStart, "call-1"))...)
	out = append(out, p.Project(toolEvent(events.KindToolEnd, "call-1"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, second, "delta"))...)
	// finalizeSDKTurn's terminal aggregate: the FINAL message's text only.
	out = append(out, p.Project(rootEvent(events.KindAssistant, second, ""))...)
	out = append(out, p.Project(rootEvent(events.KindTurnEnd, "", "completed"))...)
	return out, first, second
}

// TestAShortFirstProseBlockSurvivesAToolCall is the reported defect. The first
// message never shipped a delta (it is shorter than the hold-back window) and
// no aggregate names its block, so the drop deleted it with no trace.
func TestAShortFirstProseBlockSurvivesAToolCall(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	out, first, _ := firstShortBlockTurn(t, p)

	if got := shippedText(out); !strings.Contains(got, first) {
		t.Fatalf("the turn's FIRST prose block never reached the wire.\n"+
			"want it to contain %q\ngot deltas %q", first, got)
	}
}

// TestAShortFirstProseBlockIsRenderableInItsOwnBlock is the other half: text on
// the wire is worthless if it is filed under a block the consumer stitches into
// something else. grouping.ts keys prose on `<stream>:<step>`, so the first
// message must arrive under the segment it was spoken in - not the second
// message's.
func TestAShortFirstProseBlockIsRenderableInItsOwnBlock(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	out, first, second := firstShortBlockTurn(t, p)
	deltas, aggregates := proseByBlock(out)

	var firstBlock string
	for block, text := range deltas {
		if strings.Contains(text, first) {
			firstBlock = block
		}
	}
	if firstBlock == "" {
		t.Fatalf("no block carries the first message %q; blocks = %v", first, deltas)
	}
	if strings.Contains(deltas[firstBlock], second) {
		t.Errorf("block %s welds the first message onto the second - the reader "+
			"sees one bubble spanning a tool call", firstBlock)
	}
	if got := aggregates[firstBlock]; got != "" {
		t.Errorf("block %s got an aggregate text %q on top of its deltas - the "+
			"first message renders twice", firstBlock, got)
	}
}

// TestACompletedMessageShipsItsTailBeforeTheNextBlockClose pins defect 2. A
// hook run closes no block; it only proves the model stopped talking. The tail
// must ride out there rather than waiting for a tool start that may be a
// minute away.
func TestACompletedMessageShipsItsTailBeforeTheNextBlockClose(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := strings.Repeat("the finished answer runs on for a while. ", 10) +
		"and this is its final clause."

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, whole, "delta"))...)
	if got := shippedText(out); got == whole {
		t.Fatal("nothing was held back at all - this test cannot observe the defect")
	}
	// A stop hook runs. No block closes; the turn has not ended.
	out = append(out, p.Project(hookEvent("Stop", "guard.sh", "", "", "", false))...)

	if got := shippedText(out); got != whole {
		t.Fatalf("a completed message is still %d bytes short after the model "+
			"stopped talking; the reader sees it cut off mid-sentence until the "+
			"next block close.\ngot  %q\nwant %q", len(whole)-len(got), got, whole)
	}
}

// TestReasoningAfterProseReleasesTheHeldTail is the same release on the other
// signal that proves a prose content block ended: the model switched to
// reasoning. Reasoning passes are the long gaps in practice.
func TestReasoningAfterProseReleasesTheHeldTail(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := strings.Repeat("narrating the plan in some detail. ", 10) + "done narrating."

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, whole, "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindThinking, "now weighing the options", ""))...)

	got := ""
	for _, we := range out {
		if d, ok := we.Payload.(*AssistantDeltaPayload); ok {
			got += d.Text
		}
	}
	if got != whole {
		t.Fatalf("the prose tail is still held while the model reasons.\ngot  %q\nwant %q", got, whole)
	}
}

// TestNoProseIsLostOrDoubledPerBlock is the accounting both repairs must keep.
// Per block: the deltas plus the aggregate account for exactly the input, and
// no block delivers its words twice. This is the invariant grouping.ts stitches
// on - `(fragments === 0 || block.text === "") && aggregateText.length > 0`.
func TestNoProseIsLostOrDoubledPerBlock(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	out, first, second := firstShortBlockTurn(t, p)
	deltas, aggregates := proseByBlock(out)

	for block, agg := range aggregates {
		if agg != "" && deltas[block] != "" {
			t.Errorf("block %s carries BOTH streamed deltas and an aggregate text - "+
				"the reader is shown the same words twice", block)
		}
	}
	var all strings.Builder
	for _, text := range deltas {
		all.WriteString(text)
	}
	for _, text := range aggregates {
		all.WriteString(text)
	}
	got := all.String()
	for _, want := range []string{first, second} {
		if !strings.Contains(got, want) {
			t.Errorf("no block accounts for %q", want)
		}
	}
	if n := strings.Count(got, first); n != 1 {
		t.Errorf("the first message appears %d times across all blocks, want 1", n)
	}
}

// TestASubagentLaneKeepsItsShortFirstBlockToo holds the lane to the same
// standard. A lane's aggregate names one block for the whole run, so a lane
// that narrates, calls a tool and narrates again loses its first message by
// exactly the root path's mechanism.
func TestASubagentLaneKeepsItsShortFirstBlockToo(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	first := "Checking the lane's own config."
	second := strings.Repeat("and here is the lane's finding in full. ", 12)

	var out []WireEvent
	out = append(out, p.Project(subagentEvent(events.KindAssistant, "task-1", first, "delta"))...)
	out = append(out, p.Project(subagentEvent(events.KindSubagentStart, "task-1", "", ""))...)
	out = append(out, p.Project(subagentEvent(events.KindAssistant, "task-1", second, "delta"))...)
	out = append(out, p.Project(subagentEvent(events.KindAssistant, "task-1", second, ""))...)

	if got := shippedText(out); !strings.Contains(got, first) {
		t.Fatalf("a lane's first prose block never reached the wire.\nwant %q\ngot %q", first, got)
	}
}

// TestAShortWholeAnswerIsDeliveredExactlyOnce guards the drop that must stay.
// A turn whose only prose is shorter than the hold-back streams nothing, so
// the settled aggregate is the block's one copy of the text. Flushing the tail
// there as well would deliver the same words as a delta AND as an aggregate,
// and grouping.ts would concatenate both.
func TestAShortWholeAnswerIsDeliveredExactlyOnce(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := "A short complete answer."

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, whole, "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, whole, ""))...)
	out = append(out, p.Project(rootEvent(events.KindTurnEnd, "", "completed"))...)

	deltas, aggregates := proseByBlock(out)
	for block, agg := range aggregates {
		if agg != "" && deltas[block] != "" {
			t.Fatalf("block %s delivers %q as a delta AND %q as an aggregate - the "+
				"reader sees the answer twice", block, deltas[block], agg)
		}
	}
	var all strings.Builder
	for _, text := range deltas {
		all.WriteString(text)
	}
	for _, text := range aggregates {
		all.WriteString(text)
	}
	if n := strings.Count(all.String(), whole); n != 1 {
		t.Fatalf("the answer reaches the wire %d times, want exactly 1 (%q)", n, all.String())
	}
}

// TestAFlushedTailCountsAsStreamedForItsOwnAggregate closes the INV-1 hole the
// prose-end release opens. The flush ships a DELTA, so the block's aggregate
// must take the streamed branch and empty its text. Leaving ts.streamed false
// made the aggregate claim `fragments = 0` while a delta for the same block id
// was already on the wire, and grouping.ts - which appends deltas and then
// takes the aggregate's text whenever `fragments === 0` - rendered "HiHi there".
func TestAFlushedTailCountsAsStreamedForItsOwnAggregate(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, "Hi", "delta"))...)
	// Reasoning releases the held "Hi" as a delta.
	out = append(out, p.Project(rootEvent(events.KindThinking, "considering", ""))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, " there", "delta"))...)
	out = append(out, p.Project(rootEvent(events.KindAssistant, "Hi there", ""))...)

	deltas, aggregates := proseByBlock(out)
	for block, agg := range aggregates {
		if agg != "" && deltas[block] != "" {
			t.Fatalf("block %s ships deltas %q AND an aggregate text %q - a stitching "+
				"viewer renders %q", block, deltas[block], agg, deltas[block]+agg)
		}
	}
	var stitched strings.Builder
	for _, text := range deltas {
		stitched.WriteString(text)
	}
	for _, text := range aggregates {
		stitched.WriteString(text)
	}
	if got := stitched.String(); got != "Hi there" {
		t.Fatalf("the reader reconstructs %q, want %q", got, "Hi there")
	}
}

// TestARolledBackEmptyThinkingDeltaKeepsItsBlockStreamed pins INV-1 on the
// reasoning lane's rollback.
//
// Since the cross-fragment redactor landed, a thinking delta can ship with an
// EMPTY text. It happens for real with an open-ended pattern - the operator
// patterns for PEM blocks are written `BEGIN...(?:END|$)` - because safeCut's
// rule 2 refuses to cut inside a live match and pins the cut at the header for
// as long as the block runs. recordThinking correctly refuses to count such a
// fragment as streamed. The rollback for a batch that never reached the outbox
// did not make the same distinction: it decremented thinkingBlockFragments for
// EVERY delta payload, so undoing a delta that carried no text un-counted one
// that DID, flipped thinkingStreamed to false, and made the settled aggregate
// re-carry reasoning the viewer already holds.
func TestARolledBackEmptyThinkingDeltaKeepsItsBlockStreamed(t *testing.T) {
	pinningPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	// One fragment that ships, leaving a held tail that BEGINS with the header:
	// from here the pin sits at offset 0 and nothing more can ship.
	prose := strings.Repeat("weighing the trade-offs carefully. ", 12)
	filler := strings.Repeat("reasoning about the key material. ", 8)[:redact.StreamHoldBack-len("XBEGINX-KEY")]
	delivered := shippedText(p.Project(rootEvent(events.KindThinking,
		prose+"XBEGINX-KEY"+filler, "")))
	if delivered == "" {
		t.Fatal("nothing streamed at all - this test cannot observe the defect")
	}

	// The cut is now pinned at the header, so this fragment ships an empty
	// text - and its batch never reaches the outbox.
	held := p.Project(rootEvent(events.KindThinking, " and that is the end of it.", ""))
	if got := shippedText(held); got != "" {
		t.Fatalf("the fragment shipped %q; the repro needs it held whole", got)
	}
	p.RollbackStreaming(held)

	var msg *ThinkingMessagePayload
	for _, we := range p.Project(rootEvent(events.KindTurnEnd, "", "completed")) {
		if m, ok := we.Payload.(*ThinkingMessagePayload); ok {
			msg = m
		}
	}
	if msg == nil {
		t.Fatal("the turn's end settled no thinking block")
	}
	if strings.Contains(msg.Text, delivered) {
		t.Fatalf("the settled aggregate re-carries reasoning the viewer already holds "+
			"(fragments = %d, text = %q); the reader sees it twice", msg.Fragments, msg.Text)
	}
}

// pinningPolicy installs an OPEN-ENDED pattern, the shape an operator uses for
// a PEM block: it matches from its header to the end of the buffer, so
// safeCut's rule 2 pins the cut at the header and every later fragment ships
// nothing until the block closes.
func pinningPolicy(t *testing.T) {
	t.Helper()
	pol, err := redact.Compile([]string{`XBEGINX-KEY.*?(?:XENDX|$)`}, nil, "[redacted]")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	old := redact.Current()
	redact.SetPolicy(pol)
	t.Cleanup(func() { redact.SetPolicy(old) })
}

// TestACompletedMessageIsWholeBeforeTheTurnEnds pins the third release. A
// final message with no reasoning after it, no hook and no further tool call
// has NO block close until the turn's end, which is a whole tool stage or a
// reasoning pass away. The loop knows the message is complete the moment the
// completer returns; the projector must release the tail on that flag.
func TestACompletedMessageIsWholeBeforeTheTurnEnds(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := strings.Repeat("the finished answer runs on for a while. ", 10) +
		"and this is its final clause."

	var out []WireEvent
	out = append(out, p.Project(rootEvent(events.KindAssistant, whole, "delta"))...)
	if got := shippedText(out); got == whole {
		t.Fatal("nothing was held back at all - this test cannot observe the defect")
	}
	// The loop recorded the complete message. No block closes; no turn ends.
	out = append(out, p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete))...)

	if got := shippedText(out); got != whole {
		t.Fatalf("a completed message is still %d bytes short after the loop "+
			"reported it complete; the reader sees it cut off until the turn ends.\n"+
			"got  %q\nwant %q", len(whole)-len(got), got, whole)
	}
	// The flag releases the tail and settles THIS block (see
	// projector_assistant_settle.go); it ends nothing.
	for _, we := range out {
		if _, ok := we.Payload.(*TurnEndedPayload); ok {
			t.Fatalf("the completion flag produced a %T; it must not end the turn", we.Payload)
		}
	}

	// The turn's terminal aggregate and end follow as usual; INV-1 must hold.
	out = append(out, p.Project(rootEvent(events.KindAssistant, whole, ""))...)
	out = append(out, p.Project(rootEvent(events.KindTurnEnd, "", "completed"))...)
	deltas, aggregates := proseByBlock(out)
	for block, agg := range aggregates {
		if agg != "" && deltas[block] != "" {
			t.Fatalf("block %s ships deltas %q AND an aggregate text %q - the "+
				"reader sees it twice", block, deltas[block], agg)
		}
	}
}

// TestALaneMessageIsWholeBeforeItsRunEnds is the subagent twin: the same
// bridge fires in a lane's loop, and its flag reaches the projector with the
// lane's attribution, so the released tail must be the LANE's delta on the
// lane's block.
func TestALaneMessageIsWholeBeforeItsRunEnds(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	whole := strings.Repeat("the subagent's finished answer runs on. ", 10) +
		"and this is its final clause."

	var out []WireEvent
	out = append(out, p.Project(subagentEvent(events.KindAssistant, "task-1", whole, "delta"))...)
	if got := shippedText(out); got == whole {
		t.Fatal("nothing was held back at all - this test cannot observe the defect")
	}
	released := p.Project(subagentEvent(events.KindAssistant, "task-1", "", events.DetailAssistantComplete))
	out = append(out, released...)

	if got := shippedText(out); got != whole {
		t.Fatalf("a completed lane message is still %d bytes short after the loop "+
			"reported it complete.\ngot  %q\nwant %q", len(whole)-len(got), got, whole)
	}
	// The tail delta first, then the lane's own settle - the aggregate is
	// about to empty its text under INV-1, so the words must already be out.
	if len(released) == 0 {
		t.Fatal("the flag released nothing")
	}
	tail, ok := released[0].Payload.(*SubagentAssistantDeltaPayload)
	if !ok {
		t.Fatalf("released %T, want *SubagentAssistantDeltaPayload - a lane's "+
			"tail must not land in the root transcript", released[0].Payload)
	}
	if !strings.HasPrefix(tail.Block, "turn:1:task-1:assistant:") {
		t.Errorf("released block = %q, want the lane's block turn:1:task-1:assistant:<seg>", tail.Block)
	}
}

// TestAMessageCompleteWithNothingHeldEmitsNothing pins that the flag is a
// release and nothing else: with no tail pending it produces no wire event and
// changes no state, so the aggregate that follows still reports the whole
// text with a zero count.
func TestAMessageCompleteWithNothingHeldEmitsNothing(t *testing.T) {
	streamPolicy(t)
	p := NewProjector("sess-1", 0, proseOpts())

	if out := p.Project(rootEvent(events.KindAssistant, "", events.DetailAssistantComplete)); len(out) != 0 {
		t.Fatalf("a completion flag with nothing held produced %d wire events: %+v", len(out), out)
	}
	out := p.Project(rootEvent(events.KindAssistant, "the whole answer", ""))
	if len(out) != 1 {
		t.Fatalf("aggregate produced %d events, want 1", len(out))
	}
	msg, ok := out[0].Payload.(*AssistantMessagePayload)
	if !ok {
		t.Fatalf("got %T, want *AssistantMessagePayload", out[0].Payload)
	}
	if msg.Fragments != 0 || msg.Text != "the whole answer" {
		t.Fatalf("Fragments = %d Text = %q, want 0 and the whole answer - the "+
			"flag must not count as a shipped delta", msg.Fragments, msg.Text)
	}
}
