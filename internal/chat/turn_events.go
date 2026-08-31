package chat

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Turn lifecycle events are published from here, at the session layer, rather
// than from each surface.
//
// They used to be published by the caller: internal/clichat announced a turn
// start before calling SendUser, and nothing announced a turn end at all. That
// had two consequences.
//
// The TUI never announced anything, because only the classic REPL and line mode
// had the call. So the surface most people run was the one surface whose turns
// were invisible to a second viewer, and internal/hub's relayedKinds - which
// lists KindTurnStart, KindTurnEnd and KindError as relayable - was describing
// events that largely never fired. The hub's own receiver has handled
// KindTurnEnd and KindError since it was written, for events no code published.
//
// And a surface-published start could not carry the turn's real id. It was
// minted later, inside the session, so internal/hub documents that
// KindTurnStart's TurnID is "a throwaway, surface-local label (never the same id
// space as the TurnID on every event that follows)". A consumer therefore could
// not pair "the user asked X" with "the assistant replied Y" by id at all, and
// had to rely on single-turn-in-flight ordering instead.
//
// Publishing from beginPlainTurn/beginAgentTurn's caller fixes both at once:
// every surface is covered because every surface reaches SendUser, and the id is
// the same "turn:N" that every later event of the turn carries.

// TurnEndReason values are carried in KindTurnEnd's Detail. They are an open
// set: a consumer must treat an unrecognized reason, or an absent one from an
// older build, as an ordinary completion rather than as a failure.
const (
	TurnEndCompleted = "completed"
	TurnEndCancelled = "cancelled"
)

// CancellationCanReplaceTurnError reports whether err is the kind of failure a
// cancelled context fully explains, so a caller can report "the user stopped
// this" instead of "this failed".
//
// It lives here because the turn-end classification and the surfaces' own
// reporting must agree: internal/clichat delegates to this function rather than
// keeping a second copy, so a change to what counts as a cancellation cannot
// make the event stream and the terminal output disagree.
func CancellationCanReplaceTurnError(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// turnEventID renders the turn id every event of a turn carries. It uses the
// same prefix admission.go parses back out, so a published id round-trips
// through TurnIDFromContext.
func turnEventID(turn uint64) string {
	return turnIDPrefix + strconv.FormatUint(turn, 10)
}

// publishTurnStart announces a turn beginning, carrying the user's own
// submitted text so a second live surface can show what was typed and not only
// the reply that follows.
//
// text is the PERSISTED text, not the text sent to the provider. Those differ
// for slash skills, whose private instruction bodies are deliberately kept out
// of history; a viewer must see what the user typed, not the expansion.
func (s *Session) publishTurnStart(sessionID string, turn uint64, text string) {
	if s == nil || s.EventBus == nil {
		return
	}
	s.EventBus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		Timestamp: time.Now(),
		SessionID: sessionID,
		TurnID:    turnEventID(turn),
		Detail:    text,
	})
}

// publishTurnEnd announces the turn's terminal event: exactly one KindTurnEnd
// or one KindError, never both.
//
// A consumer treats them as equivalent terminals (see internal/clichat's hub
// sink), so emitting both would close the turn twice. The split exists so a
// viewer can tell a failure from a completion without matching on a message
// string.
func (s *Session) publishTurnEnd(ctx context.Context, sessionID string, turn uint64, err error) {
	if s == nil || s.EventBus == nil {
		return
	}
	cancelled := ctx.Err() != nil && CancellationCanReplaceTurnError(err)
	if err != nil && !cancelled {
		s.EventBus.Publish(events.Event{
			Kind:      events.KindError,
			Timestamp: time.Now(),
			SessionID: sessionID,
			TurnID:    turnEventID(turn),
			Err:       err,
		})
		return
	}
	reason := TurnEndCompleted
	if cancelled {
		reason = TurnEndCancelled
	}
	s.EventBus.Publish(events.Event{
		Kind:      events.KindTurnEnd,
		Timestamp: time.Now(),
		SessionID: sessionID,
		TurnID:    turnEventID(turn),
		Detail:    reason,
	})
}

// CurrentTurnEventID returns the id of the turn currently running, in the same
// "turn:N" form every event of that turn carries, or "" before the first turn.
//
// It exists for out-of-band producers - subagent progress arrives through a
// callback with no context, so it cannot read the turn from the caller frame
// the way TurnIDFromContext does. Reading the live counter is correct for that
// use: a subagent only runs inside the turn that dispatched it.
func (s *Session) CurrentTurnEventID() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.turnID == 0 {
		return ""
	}
	return turnEventID(s.turnID)
}
