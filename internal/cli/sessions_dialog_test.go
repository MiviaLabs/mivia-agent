package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

func seedSessions(m *tuiModel, n int) {
	m.sessions = nil
	for i := 0; i < n; i++ {
		m.sessions = append(m.sessions, chat.SessionInfo{
			Name:         fmt.Sprintf("session-%d", i),
			Model:        "test-model",
			UpdatedAt:    time.Now().Add(-time.Duration(i) * time.Hour),
			MessageCount: i * 3,
		})
	}
}

func TestSessionsDialogOpensAndLists(t *testing.T) {
	m := newReadyChatModel(30, 90)
	seedSessions(m, 4)
	m.openSessionsDialog()
	if m.sessionsDlg == nil {
		t.Fatal("/sessions must open the manager dialog")
	}
	view := stripANSI(m.View())
	for i := 0; i < 4; i++ {
		if !strings.Contains(view, fmt.Sprintf("session-%d", i)) {
			t.Fatalf("dialog missing session-%d:\n%s", i, view)
		}
	}
	for _, want := range []string{"open", "delete", "purge"} {
		if !strings.Contains(view, want) {
			t.Fatalf("dialog must advertise %q:\n%s", want, view)
		}
	}
}

func TestSessionsDialogNavigatesAndCloses(t *testing.T) {
	m := newReadyChatModel(30, 90)
	seedSessions(m, 3)
	m.openSessionsDialog()

	if m.sessionsDlg.cursor != 0 {
		t.Fatalf("cursor starts at %d", m.sessionsDlg.cursor)
	}
	m.handleChatKey("down", false)
	if m.sessionsDlg.cursor != 1 {
		t.Fatalf("down did not move the cursor: %d", m.sessionsDlg.cursor)
	}
	m.handleChatKey("up", false)
	m.handleChatKey("up", false)
	if m.sessionsDlg.cursor != 0 {
		t.Fatalf("cursor must clamp at the top: %d", m.sessionsDlg.cursor)
	}
	m.handleChatKey("esc", false)
	if m.sessionsDlg != nil {
		t.Fatal("esc must close the dialog")
	}
}

func TestSessionsDialogDeleteRequiresConfirmation(t *testing.T) {
	// Deleting a session is irreversible: a single keystroke must never do it.
	m := newReadyChatModel(30, 90)
	seedSessions(m, 3)
	m.openSessionsDialog()

	m.handleChatKey("d", false)
	if m.sessionsDlg.confirm != confirmDeleteOne {
		t.Fatalf("d must arm a confirmation, got %v", m.sessionsDlg.confirm)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "session-0") || !strings.Contains(strings.ToLower(view), "delete") {
		t.Fatalf("confirmation must name what it will destroy:\n%s", view)
	}
	// n backs out, leaving everything intact.
	m.handleChatKey("n", false)
	if m.sessionsDlg.confirm != confirmNone {
		t.Fatal("n must cancel the confirmation")
	}
	if len(m.sessions) != 3 {
		t.Fatalf("cancelled delete removed sessions: %d left", len(m.sessions))
	}
}

func TestSessionsDialogPurgeRequiresConfirmation(t *testing.T) {
	m := newReadyChatModel(30, 90)
	seedSessions(m, 3)
	m.openSessionsDialog()

	m.handleChatKey("P", false)
	if m.sessionsDlg.confirm != confirmPurgeAll {
		t.Fatalf("P must arm the purge confirmation, got %v", m.sessionsDlg.confirm)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "3") {
		t.Fatalf("purge confirmation must say how many it destroys:\n%s", view)
	}
	m.handleChatKey("esc", false)
	if m.sessionsDlg == nil || m.sessionsDlg.confirm != confirmNone {
		t.Fatal("esc must cancel the confirmation, not close the dialog")
	}
	if len(m.sessions) != 3 {
		t.Fatalf("cancelled purge removed sessions: %d left", len(m.sessions))
	}
}

func TestSessionsDialogCursorClampsAfterDelete(t *testing.T) {
	// Deleting the last row must not leave the cursor pointing past the end.
	d := &sessionsDialog{
		sessions: []chat.SessionInfo{{Name: "a"}, {Name: "b"}},
		cursor:   1,
	}
	d.removeAt(1)
	if len(d.sessions) != 1 {
		t.Fatalf("row not removed: %d", len(d.sessions))
	}
	if d.cursor != 0 {
		t.Fatalf("cursor must clamp to %d, got %d", 0, d.cursor)
	}
	d.removeAt(0)
	if len(d.sessions) != 0 || d.cursor != 0 {
		t.Fatalf("empty list state wrong: n=%d cursor=%d", len(d.sessions), d.cursor)
	}
}

func TestSessionsDialogEmptyState(t *testing.T) {
	m := newReadyChatModel(30, 90)
	m.sessions = nil
	m.openSessionsDialog()
	view := stripANSI(m.View())
	if !strings.Contains(strings.ToLower(view), "no saved sessions") {
		t.Fatalf("empty dialog must say so:\n%s", view)
	}
	// Destructive keys are inert with nothing to destroy.
	m.handleChatKey("d", false)
	m.handleChatKey("P", false)
	if m.sessionsDlg.confirm != confirmNone {
		t.Fatal("destructive keys must be inert on an empty list")
	}
}
