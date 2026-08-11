package chat

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestTurnCommitSurvivesBenignAutosave locks the defect where a benign host
// autosave (TUI periodic save, /clear) completing while a turn was in flight
// fenced the turn out of its own commit: markDurableRevision bumped the
// durable domain after EVERY successful SaveAfterTurn, so the in-flight turn's
// captured token no longer matched the current fence and commitPreparedTurn
// returned ErrStaleOperation - the turn's history was silently dropped from
// memory and never persisted. Only turn-lifecycle tokens may advance the
// durable domain, so the turn commit still adopts and persists its history.
func TestTurnCommitSurvivesBenignAutosave(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	sess := NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "sys"}, &fakeCompleter{out: "ok"})
	sess.SetSessionStore(store, mgr)
	sess.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer"},
	}

	snapshot, done, err := sess.beginAgentTurn("next", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()

	// A benign host autosave lands while the turn is in flight. Pre-fix this
	// bumps the durable domain and is the trigger that fences the turn out of
	// commitPreparedTurn.
	if err := sess.saveAfterTurn(sess.currentSaveToken()); err != nil {
		t.Fatalf("benign autosave: %v", err)
	}

	turnMsgs := append(cloneContextMessages(snapshot.messages),
		provider.Message{Role: provider.RoleUser, Content: "next"},
		provider.Message{Role: provider.RoleAssistant, Content: "turn answer"},
	)
	if err := sess.commitPreparedTurn(turnMsgs, snapshot.token, nil); err != nil {
		t.Fatalf("commitPreparedTurn after a benign autosave: %v", err)
	}

	// The turn's history is adopted into the session...
	blob := historyBlob(sess)
	if !strings.Contains(blob, "next") || !strings.Contains(blob, "turn answer") {
		t.Fatalf("turn history was not adopted into the session: %s", blob)
	}
	// ...and persisted to the rolling turn snapshot.
	names, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 {
		t.Fatalf("expected one rolling turn snapshot, got %d", len(names))
	}
	loaded, err := store.Load(names[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, m := range loaded {
		parts = append(parts, m.Role+":"+m.Content)
	}
	joined := strings.Join(parts, "|")
	if !strings.Contains(joined, "next") || !strings.Contains(joined, "turn answer") {
		t.Fatalf("turn history was not persisted to the rolling snapshot: %s", joined)
	}
}

// TestSupersededTurnStillRejectedAfterBenignAutosave pins that the durable
// fix does not weaken the real stale-turn fence: a turn superseded by a newer
// beginAgentTurn (turnID advanced) must still return ErrStaleOperation and
// adopt nothing, even when a benign autosave completes in between. The
// turnID/epoch/session/binding comparisons are unchanged by the fix.
func TestSupersededTurnStillRejectedAfterBenignAutosave(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	sess := NewSession(&config.Resolved{Model: "test-model", SystemPrompt: "sys"}, &fakeCompleter{out: "ok"})
	sess.SetSessionStore(store, mgr)
	sess.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "sys"},
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer"},
	}

	first, doneFirst, err := sess.beginAgentTurn("first", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer doneFirst()
	// A second turn supersedes the first: turnID advances.
	_, doneSecond, err := sess.beginAgentTurn("second", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer doneSecond()

	if err := sess.saveAfterTurn(sess.currentSaveToken()); err != nil {
		t.Fatalf("benign autosave: %v", err)
	}

	turnMsgs := append(cloneContextMessages(first.messages),
		provider.Message{Role: provider.RoleUser, Content: "first turn"},
		provider.Message{Role: provider.RoleAssistant, Content: "first answer"},
	)
	if err := sess.commitPreparedTurn(turnMsgs, first.token, nil); !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("superseded turn error = %v, want ErrStaleOperation", err)
	}
	if blob := historyBlob(sess); strings.Contains(blob, "first answer") {
		t.Fatalf("superseded turn adopted its history: %s", blob)
	}
}
