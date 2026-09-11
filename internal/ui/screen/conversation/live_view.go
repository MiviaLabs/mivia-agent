// This file implements the live view of a background automation
// conversation: the /resume picker can attach the screen to a session a
// run is driving off-screen, and from then on the conversation's fanned-
// out turn events (ports.LiveEventsConversation.SubscribeLive) stream
// into the transcript the way a foreground turn's do. A session with an
// active run refuses sends (runOwnsSession), so a viewer can watch but
// never interleave a user turn into the run's transcript.
//
// Lifecycle contract (see ports.LiveSubscription): the events channel a
// subscription returns is NEVER closed by the publisher; Done and Stale
// are the only lifecycle signals. Done means "stop silently" - never a
// turn end, so no queue drain and no terminal reconciliation may run on
// it. Stale means "events were dropped": reload history and resubscribe.
package conversation

import (
	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// liveEventMsg carries one fanned-out event to the screen. sub rides
// along so the read can re-arm on exactly the channel it came from.
type liveEventMsg struct {
	sessionID string
	ev        uievent.Event
	events    <-chan uievent.Event
	sub       ports.LiveSubscription
}

// liveDoneMsg: the subscription closed (viewer-side Close or shutdown).
// A silent stop - no queue drain, no terminal reconciliation.
type liveDoneMsg struct {
	sessionID string
	sub       ports.LiveSubscription
}

// liveStaleMsg: the fan-out overflowed this subscription; events were
// dropped. The view reloads history and resubscribes.
type liveStaleMsg struct {
	sessionID string
	sub       ports.LiveSubscription
}

// SetRunActivitySource wires the predicate that reports whether an
// automation run currently executes on a session id (the automation
// service, via RunTUI's wiring). nil is valid: nothing is ever refused.
func (s *Screen) SetRunActivitySource(fn func(sessionID string) bool) {
	s.runActivity = fn
}

// runOwnsSession reports whether an automation run currently executes
// on conv's session. Only a background conversation can be owned by a
// run, and only when the wired predicate says so.
func (s Screen) runOwnsSession(conv ports.Conversation) bool {
	if s.runActivity == nil || conv == nil {
		return false
	}
	bg, ok := conv.(ports.BackgroundConversation)
	if !ok || !bg.IsBackground() {
		return false
	}
	return s.runActivity(conv.ID())
}

// liveCapable reports whether conv carries the live-view capability and
// is actually a background conversation.
func liveCapable(conv ports.Conversation) (ports.LiveEventsConversation, bool) {
	if conv == nil {
		return nil, false
	}
	bg, okBg := conv.(ports.BackgroundConversation)
	le, okLive := conv.(ports.LiveEventsConversation)
	if !okBg || !okLive || !bg.IsBackground() {
		return nil, false
	}
	return le, true
}

// adoptLive arms the current conversation's live view if it does not
// already have one. The returned Cmd is the read loop's first
// continuation; nil means nothing was armed (not capable, already
// armed, or a local turn is active - its own stream covers the view
// until it ends).
func (s *Screen) adoptLive() tea.Cmd {
	if s.liveSub != nil {
		return nil
	}
	le, ok := liveCapable(s.conv)
	if !ok || s.active != nil {
		return nil
	}
	// The screen now owns the conversation: its dispatches belong on the
	// panel while the user watches (see ForegroundMarker).
	if fm, ok := s.conv.(ports.ForegroundMarker); ok {
		fm.SetForeground(true)
	}
	events, sub := le.SubscribeLive()
	s.liveSub, s.liveEvents = sub, events
	return tea.Batch(s.armLiveStatusline(), s.awaitLiveEvent(s.convID(), events, sub))
}

// armLiveStatusline gives the status row a life when the view attaches to
// a run that is ALREADY in flight.
//
// driveStatuslineFromLive arms the row from the watched turn's own
// TurnStart, which covers a viewer who was already on the session when the
// turn began. The normal case is the opposite: the operator triggers a run
// and then goes to look at it, so TurnStart is already in the past and no
// further one arrives for that turn. statusline.View draws nothing until
// Start has been called, so the row stayed blank for the whole run and a
// working automation looked dead - the same symptom the TurnStart arming
// was added to fix, one attach order over.
//
// The elapsed clock therefore measures how long this view has been
// watching, not how long the turn has run: the turn's real start is in the
// past and the freshly-attached view has no record of it. A clock that
// starts at zero is honest about what it counts; a blank row is not.
func (s *Screen) armLiveStatusline() tea.Cmd {
	if !s.runOwnsSession(s.conv) {
		return nil
	}
	// Start returns its own tick Cmd; discard it and arm through armTick,
	// the one legal clock entry point (spinner_clock.go).
	_ = s.statusline.Start("auto", s.now())
	return s.armTick()
}

// pauseLive closes the current conversation's live view before a local
// send: the turn's events would otherwise arrive twice (primary stream
// and tee). The view resubscribes when the turn ends.
func (s *Screen) pauseLive() {
	if s.liveSub != nil {
		s.liveSub.Close()
	}
	s.liveSub, s.liveEvents = nil, nil
}

// awaitLiveEvent is the live read loop's single continuation: one select
// over {events, Done, Stale}, each arm its own message. It deliberately
// does NOT go through awaitSessionEvent - a channel close there is
// defined as a turn end, and the live channel never closes.
func (s Screen) awaitLiveEvent(sessionID string, events <-chan uievent.Event, sub ports.LiveSubscription) tea.Cmd {
	return func() tea.Msg {
		select {
		case ev, ok := <-events:
			if !ok {
				// Defensive: the publisher never closes the channel; if
				// that contract ever breaks, stop silently rather than
				// fabricate a turn end.
				return liveDoneMsg{sessionID: sessionID, sub: sub}
			}
			return liveEventMsg{sessionID: sessionID, ev: ev, events: events, sub: sub}
		case <-sub.Done():
			return liveDoneMsg{sessionID: sessionID, sub: sub}
		case <-sub.Stale():
			return liveStaleMsg{sessionID: sessionID, sub: sub}
		}
	}
}

// handleLiveEvent routes one fanned-out event into its session's view
// and re-arms the live read. For the on-screen session the event flows
// through the same translation a foreground turn uses, so tool rows,
// the subagent panel, and the status line update live; for a tracked
// background session it updates that session's snapshotted state.
func (s Screen) handleLiveEvent(msg liveEventMsg) (app.Screen, tea.Cmd) {
	rearm := s.awaitLiveEvent(msg.sessionID, msg.events, msg.sub)
	if msg.sessionID == s.convID() {
		tick := s.driveStatuslineFromLive(msg.ev)
		next, cmd := s.handleTurnEventFrom(msg.ev, nil)
		return next, tea.Batch(cmd, tick, rearm)
	}
	if st, ok := s.sessions[msg.sessionID]; ok {
		st.handleTurnEvent(msg.ev)
		s.refreshTopbar()
		return s, rearm
	}
	// Untracked session: keep draining so the tee never blocks.
	return s, rearm
}

// driveStatuslineFromLive gives a watched run's turn the same status-row
// life a foreground turn gets. The foreground path starts the clock in
// sendTextWithPersisted and stops it on turn end; a live-viewed turn has
// no local send, so the turn's own framing drives it: TurnStart arms
// "auto" (the badge clips at 8 runes; the tool arms refine it to
// "running"), and TurnEnd stops it. Without this the row renders nothing
// - Start is the only thing that makes View draw - and a working run
// looks dead.
func (s *Screen) driveStatuslineFromLive(ev uievent.Event) tea.Cmd {
	switch ev.Body.(type) {
	case uievent.TurnStartBody:
		// Start returns its own tick Cmd; discard it and arm through
		// armTick, the one legal clock entry point (spinner_clock.go).
		_ = s.statusline.Start("auto", s.now())
		return s.armTick()
	case uievent.TurnEndBody:
		s.statusline.Stop()
	}
	return nil
}

// handleLiveDone clears a finished subscription. Nothing else changes:
// Done is a silent stop by contract.
func (s Screen) handleLiveDone(msg liveDoneMsg) (app.Screen, tea.Cmd) {
	if s.liveSub == msg.sub {
		s.liveSub, s.liveEvents = nil, nil
	}
	if st, ok := s.sessions[msg.sessionID]; ok && st.live == msg.sub {
		st.live, st.liveEvents = nil, nil
	}
	return s, nil
}

// handleLiveStale recovers a dropped-events view: close the stale
// subscription, repaint from the conversation's history, and
// resubscribe. For the on-screen session the repaint is LoadHistory;
// for a tracked background session the repaint happens when the user
// next visits it (its live handle is cleared, so switchConversation
// re-arms and replays).
func (s Screen) handleLiveStale(msg liveStaleMsg) (app.Screen, tea.Cmd) {
	msg.sub.Close()
	if s.liveSub == msg.sub {
		s.liveSub, s.liveEvents = nil, nil
		if s.conv != nil {
			s.LoadHistory(s.conv.History())
		}
		if le, ok := liveCapable(s.conv); ok {
			events, sub := le.SubscribeLive()
			s.liveSub, s.liveEvents = sub, events
			return s, s.awaitLiveEvent(s.convID(), events, sub)
		}
		return s, nil
	}
	if st, ok := s.sessions[msg.sessionID]; ok && st.live == msg.sub {
		st.live, st.liveEvents = nil, nil
	}
	return s, nil
}

// armSessionLive subscribes a tracked background session that has no
// live view yet and returns the read continuation. Used on switch-away
// bookkeeping and on revisit; a session that dropped its view to a
// local turn gets it back here too.
func (s *Screen) armSessionLive(id string, st *sessionState) tea.Cmd {
	if st.live != nil || st.active != nil {
		return nil
	}
	le, ok := liveCapable(st.conv)
	if !ok {
		return nil
	}
	events, sub := le.SubscribeLive()
	st.live, st.liveEvents = sub, events
	return s.awaitLiveEvent(id, events, sub)
}
