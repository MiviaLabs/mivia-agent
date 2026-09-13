package chat

// Best-effort contract of the turn journal: a journal write failure must
// warn and continue, never fail or slow the turn it exists to protect. A
// journal that could take a turn down would be worse than no journal, so
// both warning paths are driven here against a store that always fails.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// failingJournalStore wraps a real store so context enablement and the
// principal stay genuine, but every journal write fails. Only the journal
// methods are overridden; everything else delegates.
type failingJournalStore struct {
	*storage.SQLite
	err error
}

func (f failingJournalStore) AppendTurnJournalEntry(ctx context.Context, p contextstate.Principal, sessionID, turnID string, e contextstate.TurnJournalEntry) error {
	return f.err
}

func (f failingJournalStore) ClearTurnJournal(ctx context.Context, p contextstate.Principal, sessionID, turnID string) error {
	return f.err
}

// captureStderr (stale_operation_visibility_test.go) is reused here: the
// journal reports failures to stderr and nowhere else, so the warning is
// only observable that way.

// TestJournalBusEventWarnsButDoesNotFailOnAWriteError covers the append
// warning arm: the turn continues, and the operator is told the journal may
// be incomplete on crash recovery.
func TestJournalBusEventWarnsButDoesNotFailOnAWriteError(t *testing.T) {
	sess, db := newJournalTestSession(t)
	sentinel := errors.New("journal append refused")
	if err := sess.SetContextStore(failingJournalStore{SQLite: db, err: sentinel}); err != nil {
		t.Fatalf("SetContextStore: %v", err)
	}

	out := captureStderr(t, func() {
		sess.journalBusEvent(context.Background(), events.Event{
			Kind:      events.KindToolStart,
			SessionID: sess.SessionID,
			TurnID:    "turn:1",
			Name:      "read_file",
		})
	})

	if !strings.Contains(out, "turn journal append failed") {
		t.Fatalf("stderr = %q, want the append-failure warning", out)
	}
	if !strings.Contains(out, sentinel.Error()) {
		t.Fatalf("stderr = %q, want it to name the underlying error", out)
	}
}

// TestClearTurnJournalWarnsOnAFailedClear covers the clear warning arm. The
// clear runs for an already-decided turn, so a failure there must not
// propagate - it can only be reported.
func TestClearTurnJournalWarnsOnAFailedClear(t *testing.T) {
	sess, db := newJournalTestSession(t)
	sentinel := errors.New("journal clear refused")
	if err := sess.SetContextStore(failingJournalStore{SQLite: db, err: sentinel}); err != nil {
		t.Fatalf("SetContextStore: %v", err)
	}

	out := captureStderr(t, func() {
		sess.clearTurnJournal(sess.SessionID, 1)
	})

	if !strings.Contains(out, "turn journal clear failed") {
		t.Fatalf("stderr = %q, want the clear-failure warning", out)
	}
	if !strings.Contains(out, sentinel.Error()) {
		t.Fatalf("stderr = %q, want it to name the underlying error", out)
	}
}
