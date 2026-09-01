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
// It is intentionally never lossy. pumpRemoteInputs below sends with a plain
// blocking channel send, not select-default: a producer that would block
// here blocks all the way back through SyncSession.Inputs() into
// InputPoller.deliver's own delivery select, which - since the fix in
// internal/chatsync/poller.go - leaves pending_input.json on disk rather
// than clearing it when delivery cannot complete. A slow or absent UI reader
// therefore produces backpressure and, at worst, a delayed delivery on
// restart - never a silently discarded instruction.
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
			SessionID:  id,
			Body:       ri.Body,
			ReceivedAt: ri.Received,
		}
	}
}
