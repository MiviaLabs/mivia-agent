package chatsync

// RollbackSeq rolls back the sequence counter by n (e.g. on outbox append failure).
func (p *Projector) RollbackSeq(n int) {
	p.seq -= int64(n)
}

// RollbackDrops un-advances the drop watermark by delta, so a sync.dropped
// marker that was built but never stored does not consume the loss it reported.
//
// checkDrops moves the watermark when it CONSTRUCTS the marker. If the append
// that would have made it durable fails, the marker never reaches the wire while
// the watermark has already moved, so the next marker under-reports and the hole
// settled decision 6 exists to expose becomes invisible again. The watermark
// must therefore track what was STORED, exactly as the seq counter does.
func (p *Projector) RollbackDrops(delta uint64) {
	if delta >= p.lastDrops {
		p.lastDrops = 0
		return
	}
	p.lastDrops -= delta
}

// RollbackStreaming undoes the streaming bookkeeping for a batch of wire
// events that was projected but never stored.
//
// Without it a failed append is worse than a dropped event: the counters say
// the text was streamed, so INV-1 empties the settled message, and a viewer
// ends up holding neither the fragments (never stored) nor the whole answer
// (deliberately omitted). The turn's entire reply disappears while the
// transcript still looks contiguous.
//
// The counters must therefore track what was STORED, exactly as the sequence
// number and the drop watermark already do.
func (p *Projector) RollbackStreaming(wireEvents []WireEvent) {
	for _, we := range wireEvents {
		switch payload := we.Payload.(type) {
		case *AssistantDeltaPayload:
			p.rollbackOneDelta(p.turns[payload.Turn])
		case *SubagentAssistantDeltaPayload:
			if payload.Agent != nil {
				p.rollbackOneDelta(p.lanes[payload.Turn+"\x00"+payload.Agent.Task])
			}
		case *ThinkingDeltaPayload:
			p.rollbackOneThinking(p.turns[payload.Turn])
		case *SubagentThinkingDeltaPayload:
			if payload.Agent != nil {
				p.rollbackOneThinking(p.lanes[payload.Turn+"\x00"+payload.Agent.Task])
			}
		case *AssistantResetPayload:
			// A reset that never reached the wire must not have cleared the
			// producer's counters either. It did: the viewer kept every
			// fragment it already held while this side restarted from zero,
			// so the settled message reported a count far below what the
			// viewer has - and INV-1 then empties the text, losing the answer
			// through the one payload this rollback did not cover.
			p.restoreClearedStream(payload)
		}
	}
}

// restoreClearedStream undoes projectAssistantReset's clearing for a reset
// that was never stored.
//
// The count cannot be recovered exactly - it was zeroed - so this marks the
// block as streamed with an unknown count rather than claiming a wrong one.
// The settled message then carries the FULL text, which is always safe: a
// viewer that holds fragments shows the text instead of stitching, and one
// that holds none shows the answer. Losing the answer is not.
func (p *Projector) restoreClearedStream(payload *AssistantResetPayload) {
	var ts *turnState
	if payload.Agent != nil && payload.Agent.Task != "" {
		ts = p.lanes[payload.Turn+"\x00"+payload.Agent.Task]
	} else {
		ts = p.turns[payload.Turn]
	}
	if ts == nil {
		return
	}
	// Restoring the counters is impossible - projectAssistantReset zeroed them
	// before the append was attempted, so the pre-reset count is gone. Writing
	// them back as zero, which this once did, is not a rollback at all: it
	// leaves exactly the state the reset produced. Mark the block instead.
	ts.streamUnrecoverable = true
	// The step it advanced is recoverable, and must be: the replay is stamped
	// with a segment the abandoned text never used otherwise, and a consumer
	// cannot match the repair to the block it repairs. The undo is a snapshot
	// the reset took only when it advanced; a reset on a clean segment
	// advanced nothing and must restore nothing.
	if u := ts.resetUndo; u != nil {
		ts.segment, ts.segmentAssistant, ts.segmentThinking = u.segment, u.segmentAssistant, u.segmentThinking
		ts.resetUndo = nil
	}
}

func (p *Projector) rollbackOneDelta(ts *turnState) {
	if ts == nil || ts.fragments == 0 {
		return
	}
	ts.fragments--
	// Only the LAST unstored delta clears the flag. A batch that lost one of
	// several deltas is still a turn that streamed.
	if ts.fragments == 0 {
		ts.streamed = false
	}
	// A delta that never shipped must not have spent the step either.
	if ts.segmentAssistant > 0 {
		ts.segmentAssistant--
	}
	// The lost delta may have been the one that opened the segment the
	// settled aggregate names. When nothing else shipped in there, settling
	// on it publishes a block that holds nothing while the surviving
	// fragments live one segment back - fall back to them.
	//
	// The guard is `!=`, not `==`. Requiring the two to be EQUAL and then
	// assigning one to the other is a no-op: it fired only in the case where
	// there was nothing to undo, and never in the case it was written for. A
	// fall-back is possible exactly when streamSegment has moved AHEAD of the
	// segment before it, which is what `!=` says.
	if ts.segmentAssistant == 0 && ts.streamSegment != ts.prevStreamSegment {
		ts.streamSegment = ts.prevStreamSegment
	}
}

func (p *Projector) rollbackOneThinking(ts *turnState) {
	if ts == nil || ts.thinkingFragments == 0 {
		return
	}
	ts.thinkingFragments--
	if ts.segmentThinking > 0 {
		ts.segmentThinking--
	}
}

// ResetSeq resets the sequence counter to a specified sequence (e.g. on fork).
func (p *Projector) ResetSeq(seq int64) {
	p.seq = seq
}
