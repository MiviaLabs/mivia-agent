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
	if p == nil || p.notices == nil || text == "" {
		return
	}
	ev := uievent.Event{
		Kind: uievent.KindNotice,
		At:   time.Now(),
		Body: uievent.NoticeBody{Text: text},
	}
	select {
	case p.notices <- ev:
	default:
	}
}
