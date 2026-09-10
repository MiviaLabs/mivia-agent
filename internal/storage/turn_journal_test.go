package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

func newTurnJournalTestStore(t *testing.T) (*SQLite, contextstate.Principal) {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	principal, err := contextstate.NewPrincipal("workspace", "session-1", "subject")
	if err != nil {
		t.Fatal(err)
	}
	return store, principal
}

// TestAppendTurnJournalEntryRoundTrips proves the RED requirement: a step
// appended for a turn is readable back, in append order, with its payload
// and kind intact - the minimum a crash-recovery journal must guarantee.
func TestAppendTurnJournalEntryRoundTrips(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()

	entries := []contextstate.TurnJournalEntry{
		{Kind: "tool_start", Payload: []byte(`{"name":"read_file"}`)},
		{Kind: "tool_end", Payload: []byte(`{"name":"read_file","output":"ok"}`)},
	}
	for _, e := range entries {
		if err := store.AppendTurnJournalEntry(ctx, principal, "sess-A", "turn:1", e); err != nil {
			t.Fatalf("AppendTurnJournalEntry: %v", err)
		}
	}

	got, err := store.LoadTurnJournal(ctx, principal, "sess-A", "turn:1")
	if err != nil {
		t.Fatalf("LoadTurnJournal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].Kind != "tool_start" || string(got[0].Payload) != `{"name":"read_file"}` {
		t.Fatalf("entry 0 = %+v, want the tool_start step first (append order)", got[0])
	}
	if got[1].Kind != "tool_end" {
		t.Fatalf("entry 1 kind = %q, want tool_end", got[1].Kind)
	}
}

// TestLoadTurnJournalEmptyForUnknownTurn proves an unjournaled turn reads
// back as empty rather than erroring, so a recovery check can call it
// unconditionally for every turn id it is curious about.
func TestLoadTurnJournalEmptyForUnknownTurn(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	got, err := store.LoadTurnJournal(context.Background(), principal, "sess-A", "turn:999")
	if err != nil {
		t.Fatalf("LoadTurnJournal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d entries for an unjournaled turn, want 0", len(got))
	}
}

// TestClearTurnJournalRemovesOnlyThatTurn proves ClearTurnJournal is scoped
// to exactly (sessionID, turnID): a sibling turn in the same session, and
// the same turn id in a different session, must survive.
func TestClearTurnJournalRemovesOnlyThatTurn(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()
	entry := contextstate.TurnJournalEntry{Kind: "tool_start", Payload: []byte("{}")}

	must := func(sessionID, turnID string) {
		t.Helper()
		if err := store.AppendTurnJournalEntry(ctx, principal, sessionID, turnID, entry); err != nil {
			t.Fatal(err)
		}
	}
	must("sess-A", "turn:1")
	must("sess-A", "turn:2")
	must("sess-B", "turn:1")

	if err := store.ClearTurnJournal(ctx, principal, "sess-A", "turn:1"); err != nil {
		t.Fatalf("ClearTurnJournal: %v", err)
	}

	if got, err := store.LoadTurnJournal(ctx, principal, "sess-A", "turn:1"); err != nil || len(got) != 0 {
		t.Fatalf("cleared turn: got %d entries, err=%v, want 0 entries", len(got), err)
	}
	if got, err := store.LoadTurnJournal(ctx, principal, "sess-A", "turn:2"); err != nil || len(got) != 1 {
		t.Fatalf("sibling turn in same session: got %d entries, err=%v, want 1 (untouched)", len(got), err)
	}
	if got, err := store.LoadTurnJournal(ctx, principal, "sess-B", "turn:1"); err != nil || len(got) != 1 {
		t.Fatalf("same turn id, different session: got %d entries, err=%v, want 1 (untouched)", len(got), err)
	}
}

// TestListJournaledTurnsReturnsOnlyNonEmptyTurns is the recovery-check
// primitive: after a clear, a turn must not still appear as "journaled".
func TestListJournaledTurnsReturnsOnlyNonEmptyTurns(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()
	entry := contextstate.TurnJournalEntry{Kind: "tool_start", Payload: []byte("{}")}

	for _, turnID := range []string{"turn:1", "turn:2", "turn:3"} {
		if err := store.AppendTurnJournalEntry(ctx, principal, "sess-A", turnID, entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ClearTurnJournal(ctx, principal, "sess-A", "turn:2"); err != nil {
		t.Fatal(err)
	}

	turns, err := store.ListJournaledTurns(ctx, principal, "sess-A")
	if err != nil {
		t.Fatalf("ListJournaledTurns: %v", err)
	}
	want := map[string]bool{"turn:1": true, "turn:3": true}
	if len(turns) != len(want) {
		t.Fatalf("got turns %v, want exactly %v", turns, want)
	}
	for _, turnID := range turns {
		if !want[turnID] {
			t.Fatalf("unexpected journaled turn %q in %v", turnID, turns)
		}
	}
}

// TestAppendTurnJournalEntryRejectsInvalidPrincipal proves the same
// fail-closed contract every other catalog method in this package has.
func TestAppendTurnJournalEntryRejectsInvalidPrincipal(t *testing.T) {
	store, _ := newTurnJournalTestStore(t)
	err := store.AppendTurnJournalEntry(context.Background(), contextstate.Principal{}, "sess-A", "turn:1", contextstate.TurnJournalEntry{Kind: "k", Payload: []byte("{}")})
	if err == nil {
		t.Fatal("want an error for an invalid (zero) principal, got nil")
	}
}
