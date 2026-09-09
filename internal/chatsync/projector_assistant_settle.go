package chatsync

import (
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// The per-block assistant settle.
//
// A consumer marks a prose block complete on its aggregate and on nothing
// else (apps/web/src/lib/chat-sync/grouping.ts: isCompleted is set by
// assistant-message only). finalizeSDKTurn (internal/agent/agentloop_run.go)
// publishes ONE aggregate per TURN, so a message the model had finished sat
// "streaming" in the viewer through every reasoning pass and tool call that
// followed it, until the turn's end. Real wire data: 484 deltas over 9.8s on
// one block, then nothing on that block for the rest of the turn.
//
// Thinking never had this defect: thinking.message settles PER BLOCK, from
// the event that closes the block. The assistant stream now has the same
// second form, keyed on the one event that proves a message is finished
// without closing anything - the loop's message-complete flag
// (events.DetailAssistantComplete).

// The turn-end aggregate is deliberately NOT suppressed for a block settled
// here: it is the backstop for a settle that never stored (every counter
// here tracks what was STORED), it carries what a content-free flag cannot
// (the full text when nothing streamed, the true byte size), and the
// consumer is idempotent by construction. The full rationale is in
// docs/product/chat-sync-wire.md under `assistant.message`.
//
// What IS deduplicated is the flag itself: the loop flags every completed
// message, including one that only called tools, and assistantSettled keeps
// such a flag from settling the previous block again.

// settleAssistantOnComplete handles the loop's message-complete flag for the
// stream it belongs to, root or lane: release the held tail as a final delta,
// then settle the block those deltas shipped into.
func (p *Projector) settleAssistantOnComplete(env Envelope, turnID string, ev events.Event) []WireEvent {
	// The flush first, and with aggregateFollows=false: an aggregate follows
	// only when a delta shipped, and then ts.streamed is already true and the
	// tail ships regardless. With nothing shipped no settle follows, so the
	// tail must not be dropped - the turn's aggregate does not name this
	// block (see flushHeldAssistant).
	flushed := p.flushHeldAssistantOnProseEnd(env, turnID, ev)
	if isDispatched(ev) {
		return append(flushed, p.settleStreamedAssistant(env, turnID+":"+ev.AgentTask+":assistant",
			p.laneState(turnID, ev.AgentTask), TypeSubagentAssistantMessage)...)
	}
	return append(flushed, p.settleStreamedAssistant(env, turnID+":assistant", p.turn(turnID), TypeAssistantMessage)...)
}

// settleStreamedAssistant emits the settled aggregate for the block the
// stream's deltas shipped into, or nothing.
//
// It emits ONLY when deltas shipped into the named block - the same three
// terms projectAssistant reads to take the INV-1 branch. That rule is what
// makes a per-block aggregate admissible at all: under stream_assistant=false
// there are no deltas, the flag has nothing to settle, and the turn-end
// aggregate stays the sole carrier of the text. A settle there would be an
// empty message with a zero count, or the answer twice.
func (p *Projector) settleStreamedAssistant(env Envelope, blockStream string, ts *turnState, eventType string) []WireEvent {
	if ts == nil || ts.assistantSettled || !ts.streamed || ts.streamUnrecoverable || ts.blockFragments == 0 {
		return nil
	}
	ts.assistantSettled = true
	env.Block = proseBlock(blockStream, ts.streamSegment)
	// INV-1: text empty iff fragments > 0, and fragments > 0 is the gate above.
	if eventType == TypeSubagentAssistantMessage {
		payload := &SubagentAssistantMessagePayload{
			Envelope:  env,
			Fragments: ts.blockFragments,
			Bytes:     ts.blockBytes,
			Status:    "completed",
		}
		return []WireEvent{p.nextWireEvent(eventType, payload)}
	}
	payload := &AssistantMessagePayload{
		Envelope:  env,
		Fragments: ts.blockFragments,
		Bytes:     ts.blockBytes,
		Status:    "completed",
	}
	return []WireEvent{p.nextWireEvent(eventType, payload)}
}
