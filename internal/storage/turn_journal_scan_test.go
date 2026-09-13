package storage

// Failure arms of the turn journal's write and read paths. A journal that
// cannot reach its table must say so: crash recovery replays whatever it
// gets back, so a swallowed error would look like a turn that ran no steps
// at all.

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestAppendTurnJournalEntryRollsBackWhenTheSequenceLookupFails covers the
// sequence-Scan failure arm: the transaction is already open when the
// MAX(sequence) lookup runs, so a failure there must roll back rather than
// leave a half-open write. Dropping the events table after the store is
// built makes that lookup fail for real.
func TestAppendTurnJournalEntryRollsBackWhenTheSequenceLookupFails(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()
	entry := contextstate.TurnJournalEntry{Kind: "tool_start", Payload: []byte("{}")}

	if _, err := store.db.Exec(`DROP TABLE events`); err != nil {
		t.Fatalf("drop events table: %v", err)
	}

	err := store.AppendTurnJournalEntry(ctx, principal, "sess-1", "turn:1", entry)
	if err == nil {
		t.Fatal("AppendTurnJournalEntry with no events table succeeded, want the sequence lookup to fail")
	}
	if !strings.Contains(err.Error(), "events") {
		t.Fatalf("error = %v, want it to name the missing events table", err)
	}
}

// TestLoadTurnJournalSurfacesAQueryFailure covers LoadTurnJournal's query
// error arm for the same reason: a reader that cannot reach its table must
// report that, not return an empty journal a recovery pass would read as
// "this turn ran no steps".
func TestLoadTurnJournalSurfacesAQueryFailure(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()

	if _, err := store.db.Exec(`DROP TABLE events`); err != nil {
		t.Fatalf("drop events table: %v", err)
	}

	got, err := store.LoadTurnJournal(ctx, principal, "sess-1", "turn:1")
	if err == nil {
		t.Fatalf("LoadTurnJournal with no events table returned %v and no error, want a failure", got)
	}
	if got != nil {
		t.Fatalf("failed LoadTurnJournal returned %v, want nil entries", got)
	}
}

// TestListJournaledTurnsSurfacesAQueryFailure is the same contract for the
// turn listing, which session-open recovery calls to decide whether a turn
// was interrupted.
func TestListJournaledTurnsSurfacesAQueryFailure(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()

	if _, err := store.db.Exec(`DROP TABLE events`); err != nil {
		t.Fatalf("drop events table: %v", err)
	}

	got, err := store.ListJournaledTurns(ctx, principal, "sess-1")
	if err == nil {
		t.Fatalf("ListJournaledTurns with no events table returned %v and no error, want a failure", got)
	}
	if got != nil {
		t.Fatalf("failed ListJournaledTurns returned %v, want nil turns", got)
	}
}

// The rows.Scan arms inside those two readers' loops remain uncovered and
// are NOT excused in .mivia/policy/diff-coverage.json: events.kind and
// events.run_id are NOT NULL and SQLite coerces every other stored value
// into the string destinations they scan into, so a scan failure cannot be
// seeded through the real schema - but that is a reason no test exists
// yet, not a proof the branch is unreachable by any input.

// TestListJournaledTurnsReturnsTheTurnAfterASuccessfulAppend keeps the
// listing's happy path adjacent to the failure case above: the prefix trim
// must yield the bare turn id.
func TestListJournaledTurnsReturnsTheTurnAfterASuccessfulAppend(t *testing.T) {
	store, principal := newTurnJournalTestStore(t)
	ctx := context.Background()

	entry := contextstate.TurnJournalEntry{Kind: "tool_start", Payload: []byte("{}")}
	if err := store.AppendTurnJournalEntry(ctx, principal, "sess-list-scan", "turn:1", entry); err != nil {
		t.Fatalf("AppendTurnJournalEntry: %v", err)
	}

	turns, err := store.ListJournaledTurns(ctx, principal, "sess-list-scan")
	if err != nil {
		t.Fatalf("ListJournaledTurns on a healthy store: %v", err)
	}
	if len(turns) != 1 || turns[0] != "turn:1" {
		t.Fatalf("ListJournaledTurns = %v, want [turn:1]", turns)
	}
}

// TestLoadTurnJournalRejectsAnInvalidPrincipal pins the authorization
// guard ahead of any query: an unvalidated principal must never reach the
// store, since every journal read is subject-scoped.
func TestLoadTurnJournalRejectsAnInvalidPrincipal(t *testing.T) {
	store, _ := newTurnJournalTestStore(t)

	if _, err := store.LoadTurnJournal(context.Background(), contextstate.Principal{}, "s", "t"); err == nil {
		t.Fatal("LoadTurnJournal with an invalid principal succeeded, want an error")
	}
}
