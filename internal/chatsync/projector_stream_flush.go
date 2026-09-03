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
//   - Nothing shipped, so the settled aggregate carries the whole block's text
//     already. The tail is dropped, because emitting it too would show the
//     reader the same words twice.

// flushHeldAssistant emits the assistant stream's held tail as a final delta,
// or drops it when the settled aggregate is going to carry it anyway.
//
// The delta names assistantHoldSegment, the segment the held text actually
// arrived in, not the current one: the flush fires from the event that CLOSED
// the block, which has already moved on.
// terminal says no settled aggregate can be relied on to follow. A mid-turn
// block close may drop an unstreamed tail, because the turn's own aggregate
// still carries the whole text; a TURN's end may not - a turn that ended
// without an aggregate (a cancel, a provider that sent only deltas) would
// otherwise strand the tail in this buffer and the reader would never learn
// the difference between withheld prose and prose that never existed.
func (p *Projector) flushHeldAssistant(env Envelope, blockStream string, ts *turnState, lane, terminal bool) []WireEvent {
	if ts == nil || !ts.assistantStream.Pending() {
		return nil
	}
	text := ts.assistantStream.Flush()
	// streamUnrecoverable means the aggregate is about to carry the FULL text
	// so a viewer can replace what it holds; adding a tail delta on top would
	// re-show the words that replacement already includes.
	if text == "" || ts.streamUnrecoverable || (!ts.streamed && !terminal) {
		return nil
	}
	ts.fragments++
	ts.segmentAssistant++
	// The aggregate names the segment its surviving deltas used, and this
	// delta is now the last of them - so the recording has to happen here too,
	// or the settled message would point at the block before this one.
	ts.recordDeltaSegment(ts.assistantHoldSegment)
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
func (p *Projector) flushHeldAssistantFor(env Envelope, turnID string, ev events.Event, terminal bool) []WireEvent {
	if isDispatched(ev) {
		return p.flushHeldAssistant(env, turnID+":"+ev.AgentTask+":assistant",
			p.laneState(turnID, ev.AgentTask), true, terminal)
	}
	return p.flushHeldAssistant(env, turnID+":assistant", p.turn(turnID), false, terminal)
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
	// Not terminal: the turn's own settled aggregate still carries every word
	// of an unstreamed block.
	return p.flushHeldAssistantFor(env, turnID, ev, false)
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
