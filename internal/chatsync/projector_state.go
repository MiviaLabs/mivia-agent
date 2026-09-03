package chatsync

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// maxTrackedTurns bounds active turns remembered by the projector.
const maxTrackedTurns = 64

// maxTrackedLanes bounds subagent runs remembered by the projector.
//
// Lane state lives in its own map rather than sharing p.turns under a
// composite key: a wide dispatch_tasks fan-out would otherwise evict the ROOT
// turn's own state through the shared LRU, and a turn whose state is gone
// re-streams its aggregate wrongly. The two bounds are independent for the
// same reason they are bounds at all - neither may starve the other.
const maxTrackedLanes = 64

type turnState struct {
	started           bool
	done              bool
	streamed          bool
	fragments         int
	thinkingFragments int
	// thinkingPending is the RAW reasoning text accumulated for the thinking
	// block that is currently open, unredacted and untruncated. It is what
	// the settled aggregate redacts as one string, which is the only way a
	// pattern spanning two fragments can be caught. Cleared by settleThinking.
	thinkingPending string
	// thinkingBlockSegment is the segment thinkingPending belongs to, taken
	// when the block opened. The settle fires from a LATER event - a tool
	// start, a turn end - which has already advanced the counter, so the
	// current segment names a block that holds nothing.
	thinkingBlockSegment int
	// thinkingBlockFragments counts the thinking deltas that actually SHIPPED
	// TEXT into the open block, and thinkingStreamed records whether any did.
	// Counted per block, not per turn like thinkingFragments: a turn reasons
	// once per step and each step is its own block, so the per-turn index is
	// not a fragment count for any one of them.
	thinkingBlockFragments int
	thinkingStreamed       bool
	// assistantStream and thinkingStream are this stream's cross-fragment
	// redactors for the open prose block. Each holds back a bounded tail
	// (redact.StreamHoldBack) so a secret split across two deltas is still
	// caught, which is what lets deltas ship under a policy at all. Both must
	// be flushed at every block close - see flushHeldProse. A held tail nobody
	// flushes is text silently lost.
	assistantStream redact.Stream
	thinkingStream  redact.Stream
	// assistantHoldSegment is the segment the assistant stream's held tail
	// arrived in, captured when the hold OPENED. The flush fires from the
	// event that closed the block, by which time the counter has moved on,
	// and streamSegment names the last segment a delta SHIPPED into - which
	// is the previous one whenever a hold spans a tool call. Naming either
	// would file the tail under a block its own text never belonged to.
	assistantHoldSegment int
	// segment is the id of the current STEP of a turn: talk, call a tool,
	// read the result, talk again. It is what separates one utterance from
	// the next on the wire; see proseBlock. Ids come from the projector's one
	// monotonic counter (Projector.allocSegment), never from a per-state
	// counter: a lane or turn evicted from the bounded tables and re-created
	// must not re-mint an id it already used.
	segment int
	// segmentAssistant and segmentThinking count the deltas that actually
	// SHIPPED into the current segment, per stream. A tool call that follows
	// silence spends no segment, and a delta whose append failed is rolled
	// back out of its own stream's count without touching the other's - a
	// single shared flag could not tell a thinking-dirtied segment from a
	// clean one once the assistant delta was lost. Zeroed only by advanceStep.
	segmentAssistant int
	segmentThinking  int
	// resetUndo records what a reset's advance replaced, so a reset that was
	// never stored can put it back (restoreClearedStream). Nil when the reset
	// advanced nothing.
	resetUndo *segmentUndo
	// streamSegment is the segment the most recent assistant DELTA shipped
	// into. The settled aggregate names it rather than the current segment:
	// the terminal EventAssistant is published from finalizeSDKTurn, AFTER the
	// turn's last tool call has advanced the counter, so by then the current
	// segment holds nothing. Naming it shipped an empty settled message
	// carrying a fragment count, while the block holding the real text never
	// completed.
	streamSegment int
	// prevStreamSegment is the segment streamSegment held before the most
	// recent recording that changed it, one entry deep. A rollback only ever
	// undoes the batch that just failed, and the only segment a lost delta
	// can have opened is the one streamSegment named first, so one undo
	// entry is enough to fall back to the block the surviving deltas use.
	prevStreamSegment int
	// blockFragments counts the assistant deltas that SHIPPED into
	// streamSegment - the block the settled aggregate names - and is what that
	// aggregate reports as `fragments`. It mirrors thinkingBlockFragments:
	// fragments above is the turn-wide delta index and keeps counting across
	// tool calls, so on a turn with two prose blocks it claimed for the last
	// block every delta of the first. prevBlockFragments is the count
	// blockFragments held for prevStreamSegment, one entry deep for the same
	// reason prevStreamSegment is: a rollback that empties the named block
	// falls back to the previous one and must report that block's count.
	blockFragments     int
	prevBlockFragments int
	// blockBytes is the text the deltas of streamSegment's block SHIPPED, in
	// bytes, for the per-block settle's `bytes` (settleStreamedAssistant). It
	// is informational and diverges from the turn-end aggregate's `bytes` by
	// construction: that one is the raw len(ev.Content), this one is what
	// reached the wire after redaction, because the flag that settles the
	// block never sees the raw message. A lost delta is not subtracted (its
	// size is not known at rollback), but a fallback to the previous block
	// restores that block's bytes from prevBlockBytes, one entry deep like
	// prevBlockFragments, so a re-settle after a fallback does not report 0
	// or the abandoned block's size.
	blockBytes     int
	prevBlockBytes int
	// assistantSettled records that streamSegment's block already shipped its
	// per-block aggregate (settleStreamedAssistant). The loop flags EVERY
	// completed message, including one that only called tools, so without it
	// each later flag would settle the same block again. Cleared by every
	// shipped delta and by the rollback of an unstored settle.
	assistantSettled bool
	// streamUnrecoverable marks a block whose discard never reached the wire.
	// The viewer therefore still holds the abandoned attempt's fragments, and
	// this side cannot say how many - the counters were cleared before the
	// append failed. The settled message must then carry the FULL text, which
	// a viewer replaces its stitched text with. Sticky for the rest of the
	// block: a retry that streams would otherwise report a count covering only
	// its own attempt, and INV-1 would empty the one text that could repair
	// the viewer.
	streamUnrecoverable bool
}

// segmentUndo is the step state a reset replaced.
type segmentUndo struct {
	segment          int
	segmentAssistant int
	segmentThinking  int
}

// laneState returns the streaming state of one subagent run within a turn,
// creating it on first use. Bounded by maxTrackedLanes with the same
// least-recently-touched eviction p.turn uses.
func (p *Projector) laneState(turnID, task string) *turnState {
	// The separator cannot appear in either id, so two different (turn, task)
	// pairs can never collide on one key.
	key := turnID + "\x00" + task
	if ls, ok := p.lanes[key]; ok {
		p.touchLane(key)
		return ls
	}
	ls := &turnState{segment: p.allocSegment()}
	p.lanes[key] = ls
	p.laneOrder = append(p.laneOrder, key)
	for len(p.laneOrder) > maxTrackedLanes {
		delete(p.lanes, p.laneOrder[0])
		p.laneOrder = p.laneOrder[1:]
	}
	return ls
}

// retireLane forgets a subagent run's streaming state. Called when the run
// reports its terminal event: state kept past that point can only crowd out a
// live run.
//
// It matches on the TASK alone, across every turn key. Keying on the turn as
// well looked tighter and silently missed: a run whose events carry no turn id
// is filed under a SYNTHETIC turn, and resolveTurnID retires the active
// synthetic turn on turn-end, so a subagent's terminal event arriving after
// that resolves to a different synthetic turn than its own state was created
// under. The lane then survived the run that owned it. A task id identifies
// one run outright, so nothing is over-matched by ignoring the turn.
func (p *Projector) retireLane(task string) {
	if task == "" {
		return
	}
	suffix := "\x00" + task
	kept := p.laneOrder[:0]
	for _, key := range p.laneOrder {
		if strings.HasSuffix(key, suffix) {
			delete(p.lanes, key)
			continue
		}
		kept = append(kept, key)
	}
	p.laneOrder = kept
}

// A turn's end deliberately does NOT retire that turn's lanes.
//
// It looked safe - no run of a finished turn should emit again - and it is
// not: a subagent's terminal can be shed by the bounded queues that carry it,
// and this projector still projects late lane content after the turn's
// terminal (TestProjectorLateSubagentContentAfterTerminal). A wiped lane is
// recreated with streamed=false, so the late aggregate ships the whole answer
// the viewer already received delta by delta - the duplicate INV-1 exists to
// prevent, now on every turn rather than on a rare eviction.
//
// So a lane is retired only on positive evidence that its run ended
// (retireLane, from KindSubagentDone). A run whose terminal was shed stays
// resident until the LRU evicts it. That gap is real and documented rather
// than traded for a worse one; closing it needs a signal that a run is over,
// which a turn's end is not.

func (p *Projector) touchLane(key string) {
	for i, cur := range p.laneOrder {
		if cur == key {
			p.laneOrder = append(p.laneOrder[:i], p.laneOrder[i+1:]...)
			p.laneOrder = append(p.laneOrder, key)
			return
		}
	}
}

func (p *Projector) turn(id string) *turnState {
	if t, ok := p.turns[id]; ok {
		p.touchTurn(id)
		return t
	}
	t := &turnState{segment: p.allocSegment()}
	p.turns[id] = t
	p.turnOrder = append(p.turnOrder, id)
	for len(p.turnOrder) > maxTrackedTurns {
		delete(p.turns, p.turnOrder[0])
		p.turnOrder = p.turnOrder[1:]
	}
	return t
}

func (p *Projector) touchTurn(id string) {
	for i, cur := range p.turnOrder {
		if cur == id {
			p.turnOrder = append(p.turnOrder[:i], p.turnOrder[i+1:]...)
			p.turnOrder = append(p.turnOrder, id)
			return
		}
	}
}

func (p *Projector) knownTurn(id string) bool {
	_, ok := p.turns[id]
	return ok
}
