package storage

// Read-side round trip for the turn journal. The append path is covered by
// turn_journal_coverage_test.go; this file drives the two readers, which
// are what crash recovery actually calls - a journal that cannot be read
// back is the same as no journal at all.

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestTurnJournalRoundTripsEntriesInOrder pins the scan loop of
// LoadTurnJournal: entries come back in insertion order, with kind and
// payload intact, because recovery replays them in that order.
func TestTurnJournalRoundTripsEntriesInOrder(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()

	want := []contextstate.TurnJournalEntry{
		{Kind: "tool_start", Payload: []byte(`{"n":1}`)},
		{Kind: "tool_end", Payload: []byte(`{"n":2}`)},
		{Kind: "message", Payload: []byte(`{"n":3}`)},
	}
	for _, e := range want {
		if err := store.AppendTurnJournalEntry(ctx, principal, "sess-rt", "turn:1", e); err != nil {
			t.Fatalf("AppendTurnJournalEntry(%s): %v", e.Kind, err)
		}
	}

	got, err := store.LoadTurnJournal(ctx, principal, "sess-rt", "turn:1")
	if err != nil {
		t.Fatalf("LoadTurnJournal: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("LoadTurnJournal returned %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Kind != want[i].Kind {
			t.Fatalf("entry %d kind = %q, want %q (journal must replay in insertion order)", i, got[i].Kind, want[i].Kind)
		}
		if string(got[i].Payload) != string(want[i].Payload) {
			t.Fatalf("entry %d payload = %q, want %q", i, got[i].Payload, want[i].Payload)
		}
	}
}

// TestListJournaledTurnsReturnsTurnIDsForTheSession covers the
// ListJournaledTurns scan loop, including its prefix trim: the caller gets
// bare turn ids, not the namespaced run_id the rows are keyed by.
func TestListJournaledTurnsReturnsTurnIDsForTheSession(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()
	entry := contextstate.TurnJournalEntry{Kind: "tool_start", Payload: []byte("{}")}

	for _, turn := range []string{"turn:1", "turn:2"} {
		if err := store.AppendTurnJournalEntry(ctx, principal, "sess-list", turn, entry); err != nil {
			t.Fatalf("AppendTurnJournalEntry(%s): %v", turn, err)
		}
	}
	// A different session must not leak into the listing.
	if err := store.AppendTurnJournalEntry(ctx, principal, "other-sess", "turn:9", entry); err != nil {
		t.Fatalf("AppendTurnJournalEntry(other): %v", err)
	}

	turns, err := store.ListJournaledTurns(ctx, principal, "sess-list")
	if err != nil {
		t.Fatalf("ListJournaledTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("ListJournaledTurns returned %v, want exactly the 2 turns of sess-list", turns)
	}
	seen := map[string]bool{turns[0]: true, turns[1]: true}
	for _, want := range []string{"turn:1", "turn:2"} {
		if !seen[want] {
			t.Fatalf("ListJournaledTurns = %v, want it to contain the bare turn id %q", turns, want)
		}
	}
}

// ClearTurnJournal's own scoping is covered by
// TestClearTurnJournalRemovesOnlyThatTurn in turn_journal_test.go.
