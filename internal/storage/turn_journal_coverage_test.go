package storage

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func TestTurnJournalStorageCoverageGaps(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()
	entry := contextstate.TurnJournalEntry{Kind: "tool_start", Payload: []byte("{}")}

	// 1. AppendTurnJournalEntry with empty sessionID or turnID
	if err := store.AppendTurnJournalEntry(ctx, principal, "", "turn:1", entry); err == nil {
		t.Fatal("expected error on empty sessionID, got nil")
	}
	if err := store.AppendTurnJournalEntry(ctx, principal, "sess-1", "", entry); err == nil {
		t.Fatal("expected error on empty turnID, got nil")
	}

	// 2. AppendTurnJournalEntry beginWrite error (canceled context)
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.AppendTurnJournalEntry(canceledCtx, principal, "sess-1", "turn:1", entry); err == nil {
		t.Fatal("expected error on canceled context beginWrite, got nil")
	}

	// 3. AppendTurnJournalEntry ExecContext insert failure via trigger
	if _, err := store.db.Exec(`DROP TABLE events`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TABLE events(id TEXT PRIMARY KEY, run_id TEXT, sequence INTEGER, kind TEXT, payload BLOB, created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER forbid_events BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'insert blocked'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTurnJournalEntry(ctx, principal, "sess-1", "turn:1", entry); err == nil {
		t.Fatal("expected error on insert trigger abort, got nil")
	}
}

func TestTurnJournalValidationAndQueryErrors(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()

	// LoadTurnJournal invalid principal
	if _, err := store.LoadTurnJournal(ctx, contextstate.Principal{}, "s", "t"); err == nil {
		t.Fatal("expected LoadTurnJournal error on invalid principal, got nil")
	}
	// ClearTurnJournal invalid principal
	if err := store.ClearTurnJournal(ctx, contextstate.Principal{}, "s", "t"); err == nil {
		t.Fatal("expected ClearTurnJournal error on invalid principal, got nil")
	}
	// ListJournaledTurns invalid principal
	if _, err := store.ListJournaledTurns(ctx, contextstate.Principal{}, "s"); err == nil {
		t.Fatal("expected ListJournaledTurns error on invalid principal, got nil")
	}

	// QueryContext errors on closed DB
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadTurnJournal(ctx, principal, "s", "t"); err == nil {
		t.Fatal("expected LoadTurnJournal error on closed db, got nil")
	}
	if _, err := store.ListJournaledTurns(ctx, principal, "s"); err == nil {
		t.Fatal("expected ListJournaledTurns error on closed db, got nil")
	}
}
