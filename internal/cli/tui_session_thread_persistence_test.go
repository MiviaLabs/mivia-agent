package cli

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// The TUI commits every turn to the durable context store (checkpoints) but
// refreshes the chat_sessions catalog row only when its own save paths run
// (/clear, /save, the periodic autosave). A reload that prefers that catalog
// row over the newer live context row would drop every turn after the last
// catalog write - the user's messages included.

// tuiThreadStore opens one context store over a fresh temp workspace root and
// wires the session to it, so the writer (the TUI) and the reader (a fresh
// read-only session, the same surface the desktop app reads) agree on storage.
func tuiThreadStore(t *testing.T) (*chat.Session, *storage.SQLite, string) {
	t.Helper()
	// Pin HOME before any store-path resolution: openContextStore with an
	// empty StorePath falls back to GlobalContextStorePath(root), which lives
	// under $HOME/.mivia. Without this, the tests would write to the real
	// user store instead of the temp workspace.
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	store, err := openContextStore(root, config.SubagentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session := chat.NewSession(&config.Resolved{Model: "model", SystemPrompt: "sys"}, stubAgentCompleter{})
	if err := enableSessionContext(session, root, store, &config.Resolved{}); err != nil {
		t.Fatal(err)
	}
	return session, store, root
}

// startTuiThreadProgram drops a context-enabled session into a real Bubble Tea
// program with the composer ready, mirroring the shipped TUI turn path.
func startTuiThreadProgram(t *testing.T, session *chat.Session) (*scrollProgram, *string) {
	t.Helper()
	sp := startScrollProgram(t, func(m *tuiModel) {
		m.session = session
		m.toolsOn = false
		m.waiting = false
	})
	sessionID := new(string)
	sp.probe(func(m *tuiModel) { *sessionID = m.session.SessionID })
	return sp, sessionID
}

// sendTuiTurn types one user message into the composer and submits it, then
// waits for the turn to finish on the real bridge-drain path.
func sendTuiTurn(t *testing.T, sp *scrollProgram, text string) {
	t.Helper()
	sp.send(keyRunes(text))
	sp.send(tea.KeyMsg{Type: tea.KeyEnter})
	if !sp.waitUntil(4*time.Second, func(m *tuiModel) bool { return m.waiting }) {
		t.Fatalf("turn %q never started", text)
	}
	if !sp.waitUntil(8*time.Second, func(m *tuiModel) bool { return !m.waiting }) {
		t.Fatalf("turn %q never finished", text)
	}
}

// readBackThread mirrors the desktop app's read: a fresh read-only session
// over the same store loads the thread by its session id.
func readBackThread(t *testing.T, root string, store *storage.SQLite, name string) []provider.Message {
	t.Helper()
	reader := chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "test-model"}, stubAgentCompleter{})
	if err := enableSessionContext(reader, root, store, &config.Resolved{}); err != nil {
		t.Fatal(err)
	}
	if err := reader.LoadReadOnly(name); err != nil {
		t.Fatalf("read back thread %q: %v", name, err)
	}
	return reader.MessagesCopy()
}

// tuiThreadText renders the loaded thread as one string for contains checks.
func tuiThreadText(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role)
		b.WriteString(":")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// TestTuiSessionThreadSurvivesReload: with no catalog snapshot in the way, a
// reloaded thread must carry every user message the TUI accepted.
func TestTuiSessionThreadSurvivesReload(t *testing.T) {
	session, store, root := tuiThreadStore(t)
	sp, sessionID := startTuiThreadProgram(t, session)

	sendTuiTurn(t, sp, "tui question one")
	sendTuiTurn(t, sp, "tui question two")

	text := tuiThreadText(readBackThread(t, root, store, *sessionID))
	if !strings.Contains(text, "tui question one") || !strings.Contains(text, "tui question two") {
		t.Fatalf("reloaded thread lost user messages:\n%s", text)
	}
}

// TestTuiSessionThreadSurvivesStaleTurnSnapshot: the TUI's own autosave
// (periodicSaveMsg -> SaveAfterTurn) writes a chat_sessions row mid-thread.
// Turns that land after it live only in the durable context store. A reload
// must not let the older catalog row shadow the newer context row, or every
// turn after the autosave - the user's messages included - disappears from
// the thread. The same shape occurs when a line-mode sidecar (the desktop
// app) created the row and the thread continues in the TUI.
func TestTuiSessionThreadSurvivesStaleTurnSnapshot(t *testing.T) {
	session, store, root := tuiThreadStore(t)
	sp, sessionID := startTuiThreadProgram(t, session)

	sendTuiTurn(t, sp, "tui question one")
	// The TUI's autosave tick lands here: the catalog row now holds the
	// thread as of turn one.
	sp.send(periodicSaveMsg{})
	sendTuiTurn(t, sp, "tui question two")

	text := tuiThreadText(readBackThread(t, root, store, *sessionID))
	if !strings.Contains(text, "tui question one") {
		t.Fatalf("reloaded thread lost turn one:\n%s", text)
	}
	if !strings.Contains(text, "tui question two") {
		t.Fatalf("reloaded thread lost the post-autosave user message:\n%s", text)
	}
}
