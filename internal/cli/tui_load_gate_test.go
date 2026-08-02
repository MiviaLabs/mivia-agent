package cli

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestTuiLoadRefusesWhileWaiting: /load lands here from the Bubble Tea update
// goroutine while the turn runs on a worker, and it replaces the transcript,
// the binding and the tool surface. /new and /budget already gate on waiting;
// /load did not, so the only thing standing between a mid-turn /load and a
// clobbered tool surface was the session's own (previously missing) exclusion.
func TestTuiLoadRefusesWhileWaiting(t *testing.T) {
	m := newSmokeModel(t)
	m.session.SessionDir = t.TempDir()
	m.waiting = true
	before := len(m.blocks)
	if !m.handleTuiSessionStoreSlash("/load", []string{"/load", "whatever"}) {
		t.Fatal("/load was not handled")
	}
	if len(m.blocks) == before {
		t.Fatal("/load said nothing while the turn was running")
	}
	last := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(last, "/load") || !strings.Contains(last, "finish the current turn") {
		t.Fatalf("/load response = %q, want the same refusal /new gives", last)
	}
	if strings.Contains(last, "load error") {
		t.Fatal("/load attempted the load anyway")
	}
}

func TestTuiLoadRequiresAName(t *testing.T) {
	m := newSmokeModel(t)
	m.session.SessionDir = t.TempDir()
	if !m.handleTuiSessionStoreSlash("/load", []string{"/load"}) {
		t.Fatal("/load was not handled")
	}
	last := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(last, "usage: /load <name>") {
		t.Fatalf("/load with no name = %q, want the usage line", last)
	}
}

func TestTuiLoadReportsAFailure(t *testing.T) {
	m := newSmokeModel(t)
	m.session.SessionDir = t.TempDir()
	if !m.handleTuiSessionStoreSlash("/load", []string{"/load", "never-saved"}) {
		t.Fatal("/load was not handled")
	}
	last := m.blocks[len(m.blocks)-1].Text
	if !strings.Contains(last, "load error") {
		t.Fatalf("/load of a missing session = %q, want the error surfaced", last)
	}
}

// TestTuiLoadHydratesTheTranscript pins the success path: the transcript is
// replaced with the loaded session's messages, not appended to.
func TestTuiLoadHydratesTheTranscript(t *testing.T) {
	m := newSmokeModel(t)
	m.session.SessionDir = t.TempDir()
	m.session.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "saved question"},
		{Role: provider.RoleAssistant, Content: "saved answer"},
	}
	if err := m.session.Save("snap"); err != nil {
		t.Fatal(err)
	}
	m.session.Messages = nil
	m.appendInfo("stale transcript line")
	if !m.handleTuiSessionStoreSlash("/load", []string{"/load", "snap"}) {
		t.Fatal("/load was not handled")
	}
	joined := ""
	for _, block := range m.blocks {
		joined += block.Text + "\n"
	}
	if strings.Contains(joined, "stale transcript line") {
		t.Fatal("/load appended to the old transcript instead of replacing it")
	}
	if !strings.Contains(joined, "saved question") {
		t.Fatalf("/load did not hydrate the loaded messages: %q", joined)
	}
}

// TestTuiLoadReportsAContextSessionDifferently: loading a durable context
// session forks it, which the message says explicitly; a named snapshot does
// not, so the two results must not be conflated.
func TestTuiLoadReportsAContextSessionDifferently(t *testing.T) {
	m := newSmokeModel(t)
	// The durable catalog records the binding, so the session needs a real one.
	m.session = chat.NewSession(&config.Resolved{ProviderName: "fake", Model: "test-model"}, stubAgentCompleter{})
	store, err := storage.OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := enableSessionContext(m.session, t.TempDir(), store); err != nil {
		t.Fatal(err)
	}
	// A committed turn persists through the durable context path, so loading
	// by the session's own ID falls through chat_sessions to context_sessions.
	if _, err := m.session.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	name := m.session.SessionID
	if !m.handleTuiSessionStoreSlash("/load", []string{"/load", name}) {
		t.Fatal("/load was not handled")
	}
	if !m.session.LoadedContextSession() {
		t.Fatal("loading by session ID must report a durable context session")
	}
	// The info line precedes the hydrated transcript blocks.
	joined := ""
	for _, block := range m.blocks {
		joined += block.Text + "\n"
	}
	want := loadContextSessionResult(name, m.session.MessagesCount(), m.session.UserTurns())
	if !strings.Contains(joined, want) {
		t.Fatalf("/load result = %q, want %q", joined, want)
	}
}
