package chat

import (
	"context"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// firstMessageBusyRetryDelays mirrors the desktop app's own busy-retry
// cadence (mivia-agent-desktop's agent-sessions.ts BUSY_RETRY_DELAYS_MS) for
// the same reason: a mivia process's own SQLite commits are short-lived, so
// a SQLITE_BUSY collision - here, between a `sessions list` one-shot process
// and the same workspace's long-lived `mivia chat` sidecar writer, which the
// desktop app always keeps open while a thread stays open - clears within a
// few hundred ms.
var firstMessageBusyRetryDelays = []time.Duration{150 * time.Millisecond, 400 * time.Millisecond}

// isTransientBusyError distinguishes a transient write-lock collision (retry,
// this clears up) from a real failure a retry can't fix (corrupt row,
// unmounted drive). Duplicated from storage.isSQLiteBusy rather than
// imported: this package intentionally depends only on the
// contextstate.SessionFirstMessageSource interface, not the concrete SQLite
// store, so any implementation's own busy-shaped error is caught the same
// way store-agnostically.
func isTransientBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked")
}

// firstUserMessageWithRetry retries src.FirstUserMessage a couple of times
// with a short delay when it fails with a transient busy error, so a
// same-instant collision with a concurrent writer doesn't get treated the
// same as "no checkpoint yet" - the difference between a title that fills in
// on the next sidebar refresh and one that silently never does, even once
// the lock clears.
func firstUserMessageWithRetry(ctx context.Context, src contextstate.SessionFirstMessageSource, principal contextstate.Principal, sessionID string) (string, error) {
	opener, err := src.FirstUserMessage(ctx, principal, sessionID)
	for _, delay := range firstMessageBusyRetryDelays {
		if !isTransientBusyError(err) {
			break
		}
		time.Sleep(delay)
		opener, err = src.FirstUserMessage(ctx, principal, sessionID)
	}
	return opener, err
}

// titleFromFirstMessage converts a conversation opener into a display title:
// single line, whitespace collapsed, capped at 80 runes. It is display-only
// metadata and never enters the durable catalog.
func titleFromFirstMessage(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	runes := []rune(msg)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

// markFirstUserTurn best-effort persists a display title as soon as a
// session's first user message is sent, instead of waiting for the whole
// turn (user message + assistant reply) to commit. Without this, a
// just-created session has no context_sessions title and source_sequence=0,
// so it is invisible to ListSessions/resume until the first round trip
// finishes - a user who opens /resume while their first message is still in
// flight (or interrupts it) sees no trace of the session they are in.
// Gated on UserTurns()==0 so it fires exactly once per session; errors are
// ignored, since this is display metadata, not turn correctness.
func (s *Session) markFirstUserTurn(userText string) {
	if s.UserTurns() != 0 {
		return
	}
	title := titleFromFirstMessage(userText)
	if title == "" {
		title = "Untitled session"
	}
	_ = s.SetContextSessionTitle(s.SessionID, title)
}

// fillSessionTitles back-fills a display title for every untitled live context
// session in out. The title is the session's first user message, resolved
// through the store when it implements SessionFirstMessageSource. Named
// snapshots (no SessionID) and already-titled rows keep their existing name.
// Row identity and order are untouched, so internal consumers of ListSessions
// (auto-save detection, pruning, restore) see no change.
func fillSessionTitles(ctx context.Context, catalog contextstate.SessionCatalog, principal contextstate.Principal, out []SessionInfo) {
	src, ok := catalog.(contextstate.SessionFirstMessageSource)
	if !ok {
		return
	}
	for i := range out {
		if out[i].SessionID == "" || out[i].Title != "" {
			continue
		}
		opener, err := firstUserMessageWithRetry(ctx, src, principal, out[i].SessionID)
		if err != nil || opener == "" {
			continue
		}
		out[i].Title = titleFromFirstMessage(opener)
	}
}
