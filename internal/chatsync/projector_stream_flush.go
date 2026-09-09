package chatsync

import (
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Flushing the cross-fragment redactors.
//
// A streaming redactor buys its safety by WITHHOLDING a bounded tail: the last
// few hundred bytes of a block are never shipped by the delta that produced
// them, because a later delta could still complete a match across them. That is
// a delay, and a delay is only acceptable if it always ends. Every point where
// a prose block closes must therefore flush the tail - the next tool call, the
// turn's end, the turn's failure, a subagent's terminal - or the operator loses
// text outright, which is a worse defect than the one the hold-back fixes.
//
// Two shapes of flush, decided by whether anything already shipped:
//
//   - Something shipped, so INV-1 will empty the settled aggregate's text. The
//     tail must then travel as one last DELTA, before the aggregate.
//   - Nothing shipped AND the aggregate for THIS block is being emitted in the
//     same breath, so that aggregate carries the whole block's text already.
//     The tail is dropped, because emitting it too would show the reader the
//     same words twice.
//
// There is no third case. A tail is never dropped merely because the turn is
// not over: see flushHeldAssistant's aggregateFollows.

// flushHeldAssistant emits the assistant stream's held tail as a final delta,
// or drops it when the settled aggregate is going to carry it anyway.
//
// The delta names assistantHoldSegment, the segment the held text actually
// arrived in, not the current one: the flush fires from the event that CLOSED
// the block, which has already moved on.
//
// aggregateFollows says this call is the aggregate's own preamble - the caller
// is about to emit the settled message for THIS block, so an unstreamed tail
// would be shown twice and is dropped. Only projectAssistant and
// projectSubagentAssistant may pass it.
//
// It used to be spelled the other way round ("terminal"), and every mid-turn
// block close dropped an unstreamed tail on the claim that "the turn's own
// aggregate still carries the whole text". It does not, and that claim was the
// defect: finalizeSDKTurn publishes ONE terminal EventAssistant per turn
// carrying res.Final.Content - the LAST message - and it is filed under the
// one segment its own deltas used. A turn's FIRST prose block, shorter than
// redact.StreamHoldBack so no delta ever shipped, was closed by the next tool
// call and deleted with no aggregate anywhere naming it. Longer later blocks
// streamed normally, which is exactly the shape production reported.
func (p *Projector) flushHeldAssistant(env Envelope, blockStream string, ts *turnState, lane, aggregateFollows bool) []WireEvent {
	if ts == nil || !ts.assistantStream.Pending() {
		return nil
	}
	text := ts.assistantStream.Flush()
	// streamUnrecoverable means the aggregate is about to carry the FULL text
	// so a viewer can replace what it holds; adding a tail delta on top would
	// re-show the words that replacement already includes.
	if text == "" || ts.streamUnrecoverable || (!ts.streamed && aggregateFollows) {
		return nil
	}
	// This IS a delta, so it records exactly what projectAssistantDelta records
	// for one. streamed is the load-bearing half: the block's own aggregate
	// reads it to decide INV-1, and a flush that shipped words while leaving it
	// false made that aggregate claim `fragments = 0` and carry the full text
	// on top of a delta already on the wire - the viewer stitched both and
	// rendered the opening words twice.
	ts.streamed = true
	ts.fragments++
	ts.segmentAssistant++
	// The aggregate names the segment its surviving deltas used, and this
	// delta is now the last of them - so the recording has to happen here too,
	// or the settled message would point at the block before this one.
	ts.recordDeltaSegment(ts.assistantHoldSegment, len(text))
	env.Block = proseBlock(blockStream, ts.assistantHoldSegment)
	text = applyTruncation(&env, "text", text, BudgetDeltaText)
	if lane {
		payload := &SubagentAssistantDeltaPayload{Envelope: env, Text: text, Index: ts.fragments - 1}
		return []WireEvent{p.nextWireEvent(TypeSubagentAssistantDelta, payload)}
	}
	payload := &AssistantDeltaPayload{Envelope: env, Text: text, Index: ts.fragments - 1}
	return []WireEvent{p.nextWireEvent(TypeAssistantDelta, payload)}
}

// flushHeldAssistantFor flushes the assistant tail of the stream ev belongs to,
// the same root/lane split settleThinkingFor makes and for the same reason: a
// dispatched event closes its own lane's block, never the root transcript's.
func (p *Projector) flushHeldAssistantFor(env Envelope, turnID string, ev events.Event) []WireEvent {
	if isDispatched(ev) {
		return p.flushHeldAssistant(env, turnID+":"+ev.AgentTask+":assistant",
			p.laneState(turnID, ev.AgentTask), true, false)
	}
	return p.flushHeldAssistant(env, turnID+":assistant", p.turn(turnID), false, false)
}

// flushHeldAssistantOnStepClose mirrors settleThinkingOnStepClose exactly: the
// same events close an assistant block that close a thinking one, and they must
// agree, or one stream's tail would sit in the buffer while the other's shipped.
func (p *Projector) flushHeldAssistantOnStepClose(env Envelope, turnID string, ev events.Event) []WireEvent {
	switch ev.Kind {
	case events.KindToolStart, events.KindSubagentStart:
	case events.KindSubagentDone:
		if !isDispatched(ev) {
			return nil
		}
	default:
		return nil
	}
	return p.flushHeldAssistantFor(env, turnID, ev)
}

// flushHeldAssistantOnProseEnd releases the tail on an event that PROVES the
// stream's open prose content block has ended, without closing the block.
//
// The hold-back exists to guard the next fragment of the SAME content block. A
// block close was the only release, and a block closes at the next tool call or
// the turn's end - which can be a stop hook or a whole reasoning pass away. A
// message the model had finished saying therefore sat redact.StreamHoldBack
// bytes short in the viewer for that entire gap, reading as though it were
// still streaming, while the local TUI - which streams the same deltas with no
// hold-back at all - already showed it whole.
//
// Three events prove it, all on the same stream, so nothing here narrows the
// cross-fragment guarantee WITHIN a content block:
//
//   - a thinking fragment: the provider switched content block, so no further
//     byte of the prose block can arrive before it switches back;
//   - a hook run: hooks fire around tool calls and at the turn's stop, never
//     while the model is mid-utterance; and
//   - the loop's message-complete flag (KindAssistant with
//     events.DetailAssistantComplete and no content): the SDK loop emits it
//     once the completer has returned the whole message and before any tool
//     of that iteration runs. Without it a final message with no reasoning,
//     no hook and no further tool call sat redact.StreamHoldBack bytes short
//     until the turn's end.
//
// The block stays OPEN. Prose that resumes in the same segment keeps appending
// to the same block id with the next fragment index, exactly as it does after
// any other delta.
func (p *Projector) flushHeldAssistantOnProseEnd(env Envelope, turnID string, ev events.Event) []WireEvent {
	switch ev.Kind {
	case events.KindThinking, events.KindHook:
	case events.KindAssistant:
		if ev.Detail != events.DetailAssistantComplete {
			return nil
		}
	default:
		return nil
	}
	return p.flushHeldAssistantFor(env, turnID, ev)
}

// flushHeldThinking emits the thinking stream's held tail as a final delta.
// Called by settleThinking, which is the one place a thinking block closes.
func (p *Projector) flushHeldThinking(env Envelope, blockStream string, ts *turnState, messageType string) []WireEvent {
	if !ts.thinkingStream.Pending() {
		return nil
	}
	text := ts.thinkingStream.Flush()
	// Unlike the assistant path there is no terminal case to special-case:
	// settleThinking is the block's close AND its aggregate, and it always
	// emits, so an unstreamed tail is never stranded.
	// Nothing shipped, so the aggregate below redacts the whole raw block as
	// one string and carries it. That path is strictly better than a tail
	// delta: it redacts more context at once.
	if !ts.thinkingStreamed || text == "" {
		return nil
	}
	raw := len(text)
	ts.thinkingBlockFragments++
	env.Block = proseBlock(blockStream, ts.thinkingBlockSegment)
	text = applyTruncation(&env, "text", text, BudgetDeltaText)
	index := ts.thinkingFragments
	ts.thinkingFragments++
	if messageType == TypeSubagentThinkingMessage {
		payload := &SubagentThinkingDeltaPayload{Envelope: env, Bytes: raw, Index: index, Text: text}
		return []WireEvent{p.nextWireEvent(TypeSubagentThinkingDelta, payload)}
	}
	payload := &ThinkingDeltaPayload{Envelope: env, Bytes: raw, Index: index, Text: text}
	return []WireEvent{p.nextWireEvent(TypeThinkingDelta, payload)}
}
