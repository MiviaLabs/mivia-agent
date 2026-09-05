package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// The out-of-band advisory stream (ports.Notices).
//
// Turn events reach this screen through TurnHandle.Events, which closes when
// its turn ends, so nothing the harness learns between turns can travel that
// way. ports.Notices is the port for exactly that, and until now it had a
// producer on the adapter side and no reader here at all: every advisory it
// carried - a sync session stopping, and now every workflow lifecycle
// transition - was written into a channel nobody drained.
//
// Notices land in the TRANSCRIPT, not the statusline. A workflow run outlives
// many turns, so its record has to survive them: an operator who steps away
// during a two-hour run needs to scroll back and read what happened, which a
// transient status line cannot give them. The statusline stays for things
// that are true only right now.

// noticeMsg wraps one advisory event for bubbletea delivery.
type noticeMsg struct {
	event uievent.Event
}

// SetNotices supplies the out-of-band advisory channel (ports.Notices).
// A nil channel disables the reader entirely, which is the correct state for
// every embedded thread Screen and for any Screen a test builds without one.
func (s *Screen) SetNotices(ch <-chan uievent.Event) { s.notices = ch }

// awaitNotice is the read continuation for the advisory port: the same
// one-value-then-rearm shape awaitRemoteInput uses. A nil channel returns a
// nil Cmd, so no goroutine is armed and no Msg is ever produced.
func (s Screen) awaitNotice() tea.Cmd {
	if s.notices == nil {
		return nil
	}
	ch := s.notices
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return noticeMsg{event: ev}
	}
}

// handleNotice renders one advisory into the transcript and re-arms the
// reader. Re-arming FIRST is what keeps the stream alive across a render that
// drops the event: a notice whose body this screen cannot read is skipped,
// never treated as end-of-stream.
func (s Screen) handleNotice(ev uievent.Event) (app.Screen, tea.Cmd) {
	rearm := s.awaitNotice()
	body, ok := ev.Body.(uievent.NoticeBody)
	if !ok || body.Text == "" {
		return s, rearm
	}
	next, _ := s.transcript.HandleEvent(uievent.Event{
		Kind: uievent.KindNotice,
		Body: uievent.NoticeBody{Text: body.Text},
	})
	s.transcript = next
	return s, rearm
}
