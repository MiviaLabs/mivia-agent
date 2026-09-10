package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestTurnJournalNotClearedOnGenuineTurnError is the regression test for the
// bug-audit finding that finishAgentTurn's return value is not, by itself,
// reliable proof that a turn's work was durably captured: finishErroredContextTurn
// can return nil having persisted NOTHING (its no-preparation branch's
// adoptFailedTurnSnapshot silently no-ops on an invalid shape or a stale
// fence; its own commit-failure branch swallows the commit error by
// contract - see turn_finish.go). sendAgent cannot distinguish that silent
// no-op from a real success using persistErr alone, so it must be
// conservative: clear the journal only when the ORIGINAL turn outcome (err)
// was a clean success or a context cancellation, never on a genuine upstream
// error - regardless of what finishAgentTurn's own return value says. This
// fixture's particular error happens to still get durably adopted
// internally (its shape is valid), but that is exactly the point: sendAgent
// has no way to tell this case apart from the silent-no-op one, so both must
// be handled the same conservative way.
func TestTurnJournalNotClearedOnGenuineTurnError(t *testing.T) {
	upstream := errors.New("upstream 500")
	sess, store, principal, _ := newErroredContextTurnFixture(t, erroringAgentCompleter{err: upstream})
	defer store.Close()
	sess.EventBus = events.New()
	defer sess.EventBus.Close()

	var sink strings.Builder
	_, err := sess.SendUser(context.Background(), "does this stay journaled", &sink)
	if !errors.Is(err, upstream) {
		t.Fatalf("SendUser error = %v, want the original upstream error surfaced unchanged", err)
	}
	// A genuine turn error skips clearTurnJournal entirely, so nothing in
	// this path flushes the bus for us (only clearTurnJournal does, on the
	// clear-eligible outcomes) - flush explicitly so the assertion below does
	// not race the journal subscriber's own delivery goroutine.
	sess.EventBus.Flush()

	turns, loadErr := store.ListJournaledTurns(context.Background(), principal, sess.SessionID)
	if loadErr != nil {
		t.Fatalf("ListJournaledTurns: %v", loadErr)
	}
	if len(turns) == 0 {
		t.Fatal("turn journal was cleared (or never written) for a genuine turn error - the erroring turn's steps are unrecoverable")
	}
}
