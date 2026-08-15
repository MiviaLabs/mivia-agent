package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestAutoCompactionSummarySurvivesRestart pins the durable-delivery contract
// for the AUTO compaction path: the rendered context summary must be part of
// the committed checkpoint's active context AND the catalog row, so a fresh
// session resuming the saved session (a restart with no further turn) still
// receives it on both restore surfaces. commitContextTurn (internal/chat/
// turn_finish.go) appends loop.InjectedSummary() verbatim - Name
// "context-summary" included - to result.Active and s.Messages, but every
// restore path runs provider.ValidateToolPairing, which refuses NAMED user
// messages. The manual /compact path strips the name at summarizeManualCompact;
// the auto path does not. On current code the restarted session is unresumable:
// Load fails with "session message shape: invalid user message at index N" and
// the checkpoint restore fails with "active context message shape: ...".
func TestAutoCompactionSummarySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "context.db")
	store, err := storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	res := summaryWiringResolved(t, true)
	session := chat.NewSession(res, &summaryScriptedCompleter{})
	if _, err := configureSessionContext(session, dir, store, res); err != nil {
		t.Fatal(err)
	}

	// One real auto-compaction: the second SendUser hits the tightened prompt
	// budget, compacts, and the rendered summary lands in the durable
	// checkpoint (result.Active) and the live history (s.Messages).
	driveCompactingTurn(t, session)

	if last := session.Messages[len(session.Messages)-1]; !strings.Contains(last.Content, "[host-injected context summary") {
		t.Fatalf("fixture sanity: post-compaction tail = %q, want the context summary", last.Content)
	}
	assertCheckpointCarriesSummary(t, store, session)

	// Persist the catalog row the way the real chat surfaces do: SaveAfterTurn
	// is the per-turn autosave in chat_repl_linemode.go and tui_message.go.
	session.SaveAfterTurn()
	sessionID := session.SessionID
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart: a fresh process over the same database file.
	store, err = storage.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restarted := chat.NewSession(res, &summaryScriptedCompleter{})
	if _, err := configureSessionContext(restarted, dir, store, res); err != nil {
		t.Fatal(err)
	}

	// Restore surface 1: the catalog resume path (--session / /load). The
	// resumed history must still carry the host-injected context summary.
	if err := restarted.Load(sessionID); err != nil {
		t.Fatalf("restart Load(%s): %v", sessionID, err)
	}
	if !messagesCarrySummary(restarted.MessagesCopy()) {
		t.Fatalf("restarted session lost the auto-compaction summary; loaded %d messages:\n%v", len(restarted.MessagesCopy()), restarted.MessagesCopy())
	}

	// Restore surface 2: the durable checkpoint path. chat.Session.
	// loadContextSnapshot reads exactly this snapshot and validates it with
	// provider.ValidateToolPairing; the active context must restore without a
	// shape error.
	assertActiveCheckpointRestores(t, store, restarted)
}

// messagesCarrySummary reports whether any message content carries the
// host-framed context summary rendered by the summarizer.
func messagesCarrySummary(messages []provider.Message) bool {
	for _, m := range messages {
		if strings.Contains(m.Content, "[host-injected context summary") {
			return true
		}
	}
	return false
}

// checkpointActiveContext loads the session's durable checkpoint and decodes
// its active context, the exact data chat.Session.loadContextSnapshot reads.
func checkpointActiveContext(t *testing.T, store *storage.SQLite, session *chat.Session) []provider.Message {
	t.Helper()
	_, input, ok := session.ContextPreparation()
	if !ok {
		t.Fatal("session is not context-enabled")
	}
	snapshot, err := store.Load(context.Background(), input.Principal, session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var active []provider.Message
	if err := contextstate.UnmarshalCanonical(snapshot.Active.ActiveContext, &active); err != nil {
		t.Fatalf("decode active context: %v", err)
	}
	return active
}

// assertCheckpointCarriesSummary verifies the auto-compaction summary was
// durably committed into the checkpoint's active context before the restart.
func assertCheckpointCarriesSummary(t *testing.T, store *storage.SQLite, session *chat.Session) {
	t.Helper()
	if !messagesCarrySummary(checkpointActiveContext(t, store, session)) {
		t.Fatal("durable checkpoint lost the auto-compaction summary")
	}
}

// assertActiveCheckpointRestores asserts the durable checkpoint restores the
// same way the production restore path does (loadContextSnapshot ->
// validateRestoredMessages -> provider.ValidateToolPairing, failures wrapped
// as "active context message shape"). The core-memory frame exemption does
// not apply here: the auto path stamps Name "context-summary", never the
// memory-frame sentinel, and this wiring installs no memory frame.
func assertActiveCheckpointRestores(t *testing.T, store *storage.SQLite, session *chat.Session) {
	t.Helper()
	active := checkpointActiveContext(t, store, session)
	if !messagesCarrySummary(active) {
		t.Fatalf("durable checkpoint lost the auto-compaction summary; active context:\n%v", active)
	}
	if err := provider.ValidateToolPairing(active); err != nil {
		t.Fatalf("active context message shape: %v", err)
	}
}
