package uiadapter

import (
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// syncNoticeBuffer bounds the pool's advisory stream.
//
// Small on purpose: a sync notice is one line per session start and one per
// terminal stop, not a per-event feed, so a UI that is reading at all keeps
// well ahead of it. The bound exists for the UI that is NOT reading - a
// headless pool in a test, or a screen that has not armed its reader yet -
// where an unbounded channel would grow without limit and a blocking send
// would stall the sync worker that raised the notice.
const syncNoticeBuffer = 16

// Notices satisfies ports.Notices: the out-of-band advisory stream for every
// session this pool holds.
//
// It is pool-wide rather than per-conversation because the UI reads it once,
// at startup, and must keep receiving after the user switches sessions with
// /new or /resume. A per-conversation stream would need re-arming on every
// switch, and the switch path (Screen.switchConversation) returns no command
// to re-arm with, so notices from a background session would be lost exactly
// when they matter most.
func (p *SessionPool) Notices() <-chan uievent.Event {
	return p.notices
}

// pushNotice publishes one advisory line, dropping it if no reader is
// keeping up.
//
// Dropping is the contract (see ports.Notices), not a shortcut: this is
// called from chatsync's stop callback and from session construction, and
// neither may block on a UI that is slow, absent, or already gone. Losing an
// advisory line is a strictly smaller failure than wedging the uploader or
// the pool lock behind it.
func (p *SessionPool) pushNotice(text string) {
	if text == "" {
		return
	}
	p.pushNoticeEvent(uievent.Event{
		Kind: uievent.KindNotice,
		At:   time.Now(),
		Body: uievent.NoticeBody{Text: text},
	})
}

// pushNoticeEvent publishes one already-built advisory event, under the same
// lossy contract as pushNotice.
//
// The stream carries more than free text: workflow liveness travels as a
// replaceable uievent.KindWorkflowStatus that the screen routes to its status
// row instead of the transcript. Dropping one of those is harmless by
// construction - each supersedes the last, and the next heartbeat re-states
// it within the watchdog interval.
func (p *SessionPool) pushNoticeEvent(ev uievent.Event) {
	if p == nil || p.notices == nil {
		return
	}
	select {
	case p.notices <- ev:
	default:
	}
}

// WorkflowStatus is the replaceable workflow-liveness stream.
//
// Separate from Notices, and single-slot, because liveness is STATE rather
// than a sequence of messages. The controller emits a heartbeat per running
// step every 15 seconds; queuing those alongside advisories evicted the
// transitions the operator needs to read, which is the same buffer pressure
// that keeps heartbeats out of the transcript in the first place. Here an
// unread value is simply superseded, so an idle or slow reader costs one
// stale frame, never a lost notice.
func (p *SessionPool) WorkflowStatus() <-chan uievent.Event {
	return p.workflowStatusCh
}

// pushWorkflowStatus publishes the newest liveness value, replacing any
// value the UI has not read yet.
//
// Replace, not drop: the newest status is the only correct one, so discarding
// it in favour of a stale queued value would leave the row wrong until the
// next heartbeat - up to a full watchdog interval of lying about what is
// running.
func (p *SessionPool) pushWorkflowStatus(ev uievent.Event) {
	if p == nil || p.workflowStatusCh == nil {
		return
	}
	for {
		select {
		case p.workflowStatusCh <- ev:
			return
		default:
		}
		select {
		case <-p.workflowStatusCh: // drop the superseded value and retry
		default: // a reader took it first; the next loop will send
		}
	}
}
