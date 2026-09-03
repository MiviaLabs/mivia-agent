package chatsync

import (
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// The settled thinking aggregate.
//
// The assistant lane has always had two forms on the wire: streamed deltas and
// a settled whole-message aggregate the provider hands over at the end of the
// turn. Thinking had only the first, and that is why a redaction policy erased
// reasoning entirely: the per-fragment text is deliberately withheld under a
// policy (a secret split across two deltas matches neither pattern), and with
// no whole-block form to fall back to the text simply never reached the wire.
// The bytes did, so a viewer knew the agent had thought and could not say
// about what.
//
// These functions add that second form. The producer accumulates the raw text
// of the open thinking block and emits it, redacted AS ONE STRING, when the
// block closes. The suppression of streamed fragments is untouched.

// recordThinking accumulates one thinking fragment into the open block. shipped
// says whether that fragment's TEXT reached the wire, which is what decides the
// aggregate's INV-1 branch later - not whether a fragment existed.
func (ts *turnState) recordThinking(content string, segment int, shipped bool) {
	if ts.thinkingPending == "" {
		// A block's segment is fixed when it OPENS. Re-taking it per fragment
		// would let a tool call that ran mid-block move the aggregate onto a
		// segment its own deltas never used.
		ts.thinkingBlockSegment = segment
	}
	ts.thinkingPending += content
	if shipped {
		ts.thinkingBlockFragments++
		ts.thinkingStreamed = true
	}
}

// settleThinking emits the settled aggregate for one stream's open thinking
// block and clears it. It returns nil when nothing is open, so it is safe to
// call on every event that could close a block.
//
// env is the envelope of the event that CLOSED the block; only Block is
// overridden, so the aggregate carries the same turn, agent origin and clock as
// its trigger.
func (p *Projector) settleThinking(env Envelope, blockStream string, ts *turnState, eventType string) []WireEvent {
	if ts == nil || ts.thinkingPending == "" {
		return nil
	}
	raw := ts.thinkingPending
	streamed := ts.thinkingStreamed
	fragments := ts.thinkingBlockFragments
	ts.thinkingPending, ts.thinkingStreamed, ts.thinkingBlockFragments = "", false, 0

	env.Block = proseBlock(blockStream, ts.thinkingBlockSegment)
	text := ""
	switch {
	case !p.opts.IncludeThinking:
		// The host withheld reasoning text for the whole session. The
		// aggregate still ships: Bytes is the fact that the agent reasoned,
		// which is exactly what the deltas already report and what a viewer
		// shows as activity.
	case streamed:
		text = "" // INV-1: text empty iff fragments > 0.
	default:
		// The one branch this type exists for. Redacting HERE, across the
		// whole block, catches what a per-fragment redactor cannot.
		text = redactText(raw)
		text = applyTruncation(&env, "text", text, BudgetAssistantText)
	}
	if !streamed {
		fragments = 0
	}

	payload := thinkingMessagePayload(eventType, env, fragments, len(raw), text)
	return []WireEvent{p.nextWireEvent(eventType, payload)}
}

// thinkingMessagePayload builds the payload struct for the lane the aggregate
// belongs to. The two shapes are identical by design and deliberately distinct
// types: a viewer that predates the subagent vocabulary keeps a lane's
// reasoning out of the main transcript on the type prefix alone.
func thinkingMessagePayload(eventType string, env Envelope, fragments, bytes int, text string) any {
	if eventType == TypeSubagentThinkingMessage {
		return &SubagentThinkingMessagePayload{
			Envelope:  env,
			Fragments: fragments,
			Bytes:     bytes,
			Status:    "completed",
			Text:      text,
		}
	}
	return &ThinkingMessagePayload{
		Envelope:  env,
		Fragments: fragments,
		Bytes:     bytes,
		Status:    "completed",
		Text:      text,
	}
}

// settleThinkingFor closes the thinking block of the stream ev belongs to. A
// dispatched event closes its own lane's block; anything else closes the root
// turn's. Getting that wrong would settle a subagent's reasoning into the root
// transcript, the same misattribution projectByKind guards for assistant prose.
func (p *Projector) settleThinkingFor(env Envelope, turnID string, ev events.Event) []WireEvent {
	if isDispatched(ev) {
		return p.settleThinking(env, turnID+":"+ev.AgentTask+":thinking",
			p.laneState(turnID, ev.AgentTask), TypeSubagentThinkingMessage)
	}
	return p.settleThinking(env, turnID+":thinking", p.turn(turnID), TypeThinkingMessage)
}

// settleThinkingOnStepClose settles the thinking block that ev closes, or
// nothing. It exists so the tool and subagent arms of projectByKind can call
// one thing: most subagent kinds are run lifecycle and close no block at all.
//
// Callers must invoke it BEFORE closeStepOnToolStart, which advances the
// segment the aggregate has to name, and must place what it returns BEFORE
// the event's own wire events - a viewer renders in arrival order, and
// reasoning belongs above the tool card it explains.
func (p *Projector) settleThinkingOnStepClose(env Envelope, turnID string, ev events.Event) []WireEvent {
	switch ev.Kind {
	case events.KindToolStart, events.KindSubagentStart:
		// The step boundary, exactly as closeStepOnToolStart defines it: the
		// model stopped reasoning and acted, so the block it was filling is
		// finished. A tool END closes nothing - the reasoning that follows a
		// result belongs to the step the start already opened.
	case events.KindSubagentDone:
		// A run's terminal RETIRES its lane state, so anything still pending
		// there is about to be forgotten. Only an attributed terminal, though:
		// an unattributed one would settle the ROOT turn's block, which a
		// subagent finishing has no business closing.
		if !isDispatched(ev) {
			return nil
		}
	default:
		return nil
	}
	return p.settleThinkingFor(env, turnID, ev)
}
