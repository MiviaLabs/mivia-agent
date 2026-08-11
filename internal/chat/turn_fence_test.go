package chat

import (
	"context"
	"errors"
	"io"
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

// TestTurnStartAdmissionPublicationDoesNotFenceOwnTurn locks the defect where a
// start-of-turn admission publication fenced the turn out of its own commit:
// beginAgentTurn captures the turn's OperationToken before surfaceForTurnStart
// publishes a stage an earlier boundary left behind, and that publication swaps
// the binding surface and bumps the operation fence (TryPublishAgentSurface ->
// invalidateLocked). commitPreparedTurn then sees a stale token, returns
// ErrStaleOperation, and sendAgent swallows it - the turn's user message and
// reply are silently lost from memory and never persisted. The fix re-captures
// the turn's token pinned to its own turn id after the publication, so the
// commit runs under the fence the loop actually executed on.
func TestTurnStartAdmissionPublicationDoesNotFenceOwnTurn(t *testing.T) {
	store := newTestStore(t)
	mgr := NewSaveManager(store, "test-model", "test-provider")
	sess := agentTurnSession(t, &fakeCompleter{out: "turn answer"})
	sess.SetSessionStore(store, mgr)
	// The widener stands in for the host's rebuild (cli.newSurfaceWidener): it
	// must build the candidate registry - core tool plus the admitted names -
	// BEFORE publishing. Publishing the request as-is would install a nil
	// registry as the live surface, and the agent loop would fail with "nil
	// tools" before the start-of-turn publication could be exercised.
	widener := &replayWidener{sess: sess}
	sess.SetSurfaceWidener(widener.fn)

	// Advance the session to turn 1 so the staged admission below is owned by a
	// turn strictly before the SendUser turn: publication at the SendUser turn's
	// start is the earliest safe point, because a stage owned by the current,
	// not-yet-run turn stays deferred for its own boundary (D7).
	if _, doneFirst, err := sess.beginAgentTurn("first", nil); err != nil {
		t.Fatal(err)
	} else {
		doneFirst()
	}
	if _, err := sess.StageToolAdmission([]string{"grep"}, 1); err != nil {
		t.Fatalf("stage turn-1 admission: %v", err)
	}

	reply, err := sess.SendUser(context.Background(), "next", io.Discard)
	if err != nil {
		t.Fatalf("SendUser: %v", err)
	}
	if reply != "turn answer" {
		t.Fatalf("reply = %q, want the completer output", reply)
	}
	if calls, _ := widener.snapshot(); len(calls) != 1 {
		t.Fatalf("widener ran %d times, want exactly the one start-of-turn publication", len(calls))
	}

	// The turn's history is adopted into the session...
	blob := historyBlob(sess)
	if !strings.Contains(blob, "next") || !strings.Contains(blob, "turn answer") {
		t.Fatalf("turn history was not adopted into the session: %s", blob)
	}
	// ...and persisted to exactly one rolling turn snapshot.
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

// TestSupersededTurnStillRejectedAfterTurnStartPublication pins that the
// start-of-turn re-capture does not weaken the genuine stale-turn fence: a turn
// superseded by a newer beginAgentTurn must still return ErrStaleOperation and
// adopt nothing, even after the start-of-turn publication path is engaged. The
// re-captured token pins the turn's OWN id (captureTurnToken), so a newer
// session turn id can never validate the older turn's commit.
func TestSupersededTurnStillRejectedAfterTurnStartPublication(t *testing.T) {
	sess := agentTurnSession(t, &fakeCompleter{out: "turn answer"})
	widener := &recordingWidener{}
	sess.SetSurfaceWidener(widener.fn)

	// Advance to turn 1 so the staged admission is owned by a strictly earlier
	// turn; turn 2 then runs the start-of-turn publication path and turn 3
	// supersedes it.
	if _, doneFirst, err := sess.beginAgentTurn("first", nil); err != nil {
		t.Fatal(err)
	} else {
		doneFirst()
	}
	if _, err := sess.StageToolAdmission([]string{"grep"}, 1); err != nil {
		t.Fatalf("stage turn-1 admission: %v", err)
	}
	turn2, done2, err := sess.beginAgentTurn("second", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done2()
	if _, done3, err := sess.beginAgentTurn("third", nil); err != nil {
		t.Fatal(err)
	} else {
		defer done3()
	}

	// The publication path defers with a sibling turn active, so the widener is
	// never asked to build a candidate surface.
	_, _, token := sess.surfaceForTurnStart(turn2, nil)
	if widener.count() != 0 {
		t.Fatalf("widener ran %d times, want none: publication defers while turn 3 is active", widener.count())
	}

	turnMsgs := append(cloneContextMessages(turn2.messages),
		provider.Message{Role: provider.RoleUser, Content: "second turn"},
		provider.Message{Role: provider.RoleAssistant, Content: "second answer"},
	)
	if err := sess.commitPreparedTurn(turnMsgs, token, nil); !errors.Is(err, ErrStaleOperation) {
		t.Fatalf("superseded turn error = %v, want ErrStaleOperation", err)
	}
	if blob := historyBlob(sess); strings.Contains(blob, "second answer") {
		t.Fatalf("superseded turn adopted its history: %s", blob)
	}
}
