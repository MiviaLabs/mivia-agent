package uiadapter

import (
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// remoteInputBuffer bounds the pool-wide fan-in channel. Unlike
// syncNoticeBuffer this is NOT a "drop if nobody's reading" buffer - see
// RemoteInputs' doc comment - it exists only to smooth bursts across
// multiple pooled sessions delivering at once; a reader that falls behind
// applies backpressure all the way down to the originating InputPoller
// rather than losing anything.
const remoteInputBuffer = 16

// RemoteInputs satisfies ports.RemoteInputs: the pool-wide steering stream
// fed by every pooled session's chatsync.InputPoller, tagged with which
// LOCAL chat session each instruction targets.
//
// It is never DROPPED. pumpRemoteInputs below sends with a plain blocking
// channel send, not select-default: a producer that would block here blocks
// all the way back through SyncSession.Inputs() into InputPoller.deliver's
// own delivery select, which - since the fix in internal/chatsync/poller.go
// - leaves pending_input.json on disk rather than clearing it when delivery
// cannot complete. A slow or absent UI reader therefore produces
// backpressure rather than an explicit drop.
//
// That is a narrower guarantee than "never lost". Once an input crosses
// InputPoller's own inputCh, chatsync.InputPoller.deliver's doc comment
// spells out the residual risk this buffer inherits: a value sitting HERE,
// or in the conversation Screen's own queue once past this channel, exists
// only in process memory until conv.Send actually runs. A crash in that
// window is a real, if narrow, loss window - not covered by the
// exactly-once ledger, which only guards against REdelivering something
// already handed off, not against a handoff that was never acted on.
func (p *SessionPool) RemoteInputs() <-chan ports.RemoteInputEvent {
	return p.remoteInputs
}

// pumpRemoteInputs forwards one attached session's chatsync.RemoteInput
// stream onto the pool-wide RemoteInputs channel, tagging each with the
// LOCAL chat session id (id) - the same id conversation.Screen's
// s.convID()/s.sessions map already key on for turn events
// (uievent.EventMsg.SessionID). It returns when inputs closes, which
// chatsync.InputPoller does exactly once, when the sync session stops.
func (p *SessionPool) pumpRemoteInputs(id string, inputs <-chan chatsync.RemoteInput) {
	for ri := range inputs {
		p.remoteInputs <- ports.RemoteInputEvent{
			ID:         ri.ID,
			Kind:       ri.Kind,
			SessionID:  id,
			Body:       ri.Body,
			ReceivedAt: ri.Received,
		}
	}
}
