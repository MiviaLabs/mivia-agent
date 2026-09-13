package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// newJournalTestSession builds a context-enabled Session (wireCatalogSession,
// context_catalog_test.go) with its own live event bus, so the durable turn
// journal has both halves it needs: a store implementing
// contextstate.TurnJournal, and a bus to subscribe to.
func newJournalTestSession(t *testing.T) (*Session, *storage.SQLite) {
	t.Helper()
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := wireCatalogSession(t, store, &config.Resolved{ProviderName: "ollama", Model: "llama3.1:8b"}, &fakeCompleter{out: "hi"})
	session.EventBus = events.New()
	t.Cleanup(session.EventBus.Close)
	return session, store
}

// TestJournaledKindsIsExactlyTheDiscreteStepSet pins the scope decision in
// turn_journal.go's doc comment: only discrete, low-frequency step
// boundaries are journaled. If this test breaks because a delta kind was
// added, re-read that comment before "fixing" it - the bus drops the OLDEST
// queued event on overflow with no signal, so a per-token write rate would
// silently defeat the journal's own purpose.
func TestJournaledKindsIsExactlyTheDiscreteStepSet(t *testing.T) {
	want := map[events.Kind]bool{
		events.KindTurnStart: true,
		events.KindToolStart: true,
		events.KindToolEnd:   true,
		events.KindAssistant: true,
		events.KindThinking:  true,
	}
	if len(journaledKinds) != len(want) {
		t.Fatalf("journaledKinds has %d entries, want %d: %v", len(journaledKinds), len(want), journaledKinds)
	}
	for _, k := range journaledKinds {
		if !want[k] {
			t.Fatalf("journaledKinds contains unexpected kind %q", k)
		}
	}
}

func TestJournalBusEventRecordsASubscribedStep(t *testing.T) {
	session, store := newJournalTestSession(t)
	session.ensureTurnJournalSubscribed()

	session.EventBus.Publish(events.Event{
		Kind:       events.KindToolStart,
		SessionID:  session.SessionID,
		TurnID:     "turn:1",
		ToolCallID: "call-1",
		Name:       "read_file",
		Input:      `{"path":"a.go"}`,
	})
	session.EventBus.Flush()

	entries, err := store.LoadTurnJournal(context.Background(), session.contextPrincipal, session.SessionID, "turn:1")
	if err != nil {
		t.Fatalf("LoadTurnJournal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d journal entries, want 1", len(entries))
	}
	if entries[0].Kind != string(events.KindToolStart) {
		t.Fatalf("entry kind = %q, want %q", entries[0].Kind, events.KindToolStart)
	}
	if !strings.Contains(string(entries[0].Payload), `"tool_call_id":"call-1"`) || !strings.Contains(string(entries[0].Payload), `"name":"read_file"`) {
		t.Fatalf("entry payload missing expected fields: %s", entries[0].Payload)
	}
}

// TestJournalBusEventSkipsAssistantDeltasButKeepsTheSettledMessage is the
// regression test for the bug-audit finding that KindAssistant overloads one
// Kind with streaming deltas, a content-free "complete" signal, and the
// turn's one settled message - only the last is a discrete step worth a
// durable write.
func TestJournalBusEventSkipsAssistantDeltasButKeepsTheSettledMessage(t *testing.T) {
	session, store := newJournalTestSession(t)
	session.ensureTurnJournalSubscribed()

	for i := 0; i < 5; i++ {
		session.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: session.SessionID, TurnID: "turn:3", Detail: "delta", Content: "partial"})
	}
	session.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: session.SessionID, TurnID: "turn:3", Detail: events.DetailAssistantComplete})
	session.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: session.SessionID, TurnID: "turn:3", Content: "the full settled reply"})
	session.EventBus.Flush()

	entries, err := store.LoadTurnJournal(context.Background(), session.contextPrincipal, session.SessionID, "turn:3")
	if err != nil {
		t.Fatalf("LoadTurnJournal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d journal entries (5 deltas + 1 complete-signal + 1 settled published), want exactly 1 (the settled message only): %+v", len(entries), entries)
	}
	if !strings.Contains(string(entries[0].Payload), "the full settled reply") {
		t.Fatalf("the one journaled entry is not the settled message: %s", entries[0].Payload)
	}
}

func TestJournalBusEventIgnoresEventsWithNoSessionOrTurnID(t *testing.T) {
	session, store := newJournalTestSession(t)
	session.ensureTurnJournalSubscribed()

	session.EventBus.Publish(events.Event{Kind: events.KindToolStart, Name: "orphan"})
	session.EventBus.Flush()

	turns, err := store.ListJournaledTurns(context.Background(), session.contextPrincipal, session.SessionID)
	if err != nil {
		t.Fatalf("ListJournaledTurns: %v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("got journaled turns %v for an event with no session/turn id, want none", turns)
	}
}

func TestEnsureTurnJournalSubscribedIsIdempotent(t *testing.T) {
	session, store := newJournalTestSession(t)
	session.ensureTurnJournalSubscribed()
	session.ensureTurnJournalSubscribed()
	session.ensureTurnJournalSubscribed()

	session.EventBus.Publish(events.Event{Kind: events.KindToolStart, SessionID: session.SessionID, TurnID: "turn:1", Name: "once"})
	session.EventBus.Flush()

	entries, err := store.LoadTurnJournal(context.Background(), session.contextPrincipal, session.SessionID, "turn:1")
	if err != nil {
		t.Fatalf("LoadTurnJournal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d journal entries after 3 subscribe calls and 1 publish, want exactly 1 (no duplicate subscriptions)", len(entries))
	}
}

func TestClearTurnJournalRemovesTheTurnsSteps(t *testing.T) {
	session, store := newJournalTestSession(t)
	session.ensureTurnJournalSubscribed()

	session.EventBus.Publish(events.Event{Kind: events.KindToolStart, SessionID: session.SessionID, TurnID: "turn:7", Name: "read_file"})
	session.EventBus.Publish(events.Event{Kind: events.KindToolEnd, SessionID: session.SessionID, TurnID: "turn:7", Name: "read_file"})

	session.clearTurnJournal(session.SessionID, 7)

	entries, err := store.LoadTurnJournal(context.Background(), session.contextPrincipal, session.SessionID, "turn:7")
	if err != nil {
		t.Fatalf("LoadTurnJournal: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d journal entries after clear, want 0", len(entries))
	}
}

// TestClearTurnJournalDoesNotLoseAConcurrentlyPublishedStep is the direct
// regression test for the architecture-review finding that motivated
// Flush-before-clear: Bus.Publish is non-blocking, so without an explicit
// flush the last event of a turn can still be in the subscriber's queue when
// clearTurnJournal runs. This does not reproduce the race deterministically
// (that would need instrumenting the bus's internal queue), but it pins the
// observable contract: publish-then-immediately-clear must never leave the
// published step behind un-cleared, which would only happen if a future
// change removed the Flush call.
func TestClearTurnJournalDoesNotLoseAConcurrentlyPublishedStep(t *testing.T) {
	session, store := newJournalTestSession(t)
	session.ensureTurnJournalSubscribed()

	for i := 0; i < 20; i++ {
		session.EventBus.Publish(events.Event{Kind: events.KindToolStart, SessionID: session.SessionID, TurnID: "turn:9", Name: "step"})
	}
	session.clearTurnJournal(session.SessionID, 9)

	entries, err := store.LoadTurnJournal(context.Background(), session.contextPrincipal, session.SessionID, "turn:9")
	if err != nil {
		t.Fatalf("LoadTurnJournal: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d journal entries after clear (want 0) - Flush-before-clear may be missing or broken", len(entries))
	}
}
