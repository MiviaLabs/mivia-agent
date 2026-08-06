package cli

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestSlashClearBlocksWhileBusy verifies /clear refuses to run while an agent
// turn is in flight (m.waiting). Without the guard, /clear would SaveAfterTurn
// then Clear, silently discarding the in-flight turn while the transcript kept
// rendering it as completed - the same race /new and /load already gate on.
func TestSlashClearBlocksWhileBusy(t *testing.T) {
	sess := makeTestSessionWithStore(t, "m")
	addTurn(t, sess, "q", "a")
	beforeMsgs := sess.MessagesCopy()

	m := newReadyChatModel(30, 90)
	m.session = sess
	m.waiting = true // a turn is running
	// Transcript state an unguarded /clear would wipe.
	m.blocks = []ChatBlock{{ID: "b1", Kind: ChatBlockAssistant, Text: "answer-1"}}
	m.msgOffset = 3

	if !m.handleSlash("/clear") {
		t.Fatal("/clear must still be handled (with a refusal notice) while busy")
	}

	// The refusal notice is visible and the pre-existing block survived.
	noticed := false
	kept := false
	for _, b := range m.blocks {
		if strings.Contains(b.Text, "(finish the current turn before /clear)") {
			noticed = true
		}
		if b.ID == "b1" {
			kept = true
		}
	}
	if !noticed {
		t.Fatal("busy /clear must show the refusal notice")
	}
	if !kept {
		t.Fatal("busy /clear must retain the existing transcript blocks")
	}
	if m.msgOffset != 3 {
		t.Fatalf("busy /clear must not reset msgOffset, got %d", m.msgOffset)
	}

	// Session untouched: identical messages, no SaveAfterTurn persistence.
	afterMsgs := sess.MessagesCopy()
	if len(afterMsgs) != len(beforeMsgs) {
		t.Fatalf("message count changed during busy /clear: %d -> %d", len(beforeMsgs), len(afterMsgs))
	}
	for i := range beforeMsgs {
		if beforeMsgs[i].Role != afterMsgs[i].Role || beforeMsgs[i].Content != afterMsgs[i].Content {
			t.Fatalf("message %d changed during busy /clear: %+v -> %+v", i, beforeMsgs[i], afterMsgs[i])
		}
	}
	if sessions, _ := sess.ListSessions(); len(sessions) != 0 {
		t.Fatalf("busy /clear should not persist, but found %d sessions", len(sessions))
	}
}

// TestSlashClearIdleStillClears verifies the busy guard does not block the
// normal idle /clear: history is persisted first (SaveAfterTurn), then the
// transcript and session are cleared and the notice is shown.
func TestSlashClearIdleStillClears(t *testing.T) {
	sess := makeTestSessionWithStore(t, "m")
	addTurn(t, sess, "q", "a")

	m := newReadyChatModel(30, 90)
	m.session = sess
	m.waiting = false
	m.blocks = []ChatBlock{{ID: "b1", Kind: ChatBlockAssistant, Text: "answer-1"}}
	m.msgOffset = 3

	if !m.handleSlash("/clear") {
		t.Fatal("/clear must be handled while idle")
	}

	// Session cleared back to the system prompt only.
	if n := sess.MessagesCount(); n != 1 {
		t.Fatalf("expected 1 message (system prompt) after idle /clear, got %d", n)
	}
	if got := sess.Messages[0].Role; got != provider.RoleSystem {
		t.Fatalf("expected system prompt to survive /clear, got role %v", got)
	}
	// Transcript wiped (only the slash echo + notice remain).
	for _, b := range m.blocks {
		if b.ID == "b1" {
			t.Fatal("idle /clear must wipe existing transcript blocks")
		}
	}
	if m.msgOffset != 0 {
		t.Fatalf("idle /clear must reset msgOffset to 0, got %d", m.msgOffset)
	}
	noticed := false
	for _, b := range m.blocks {
		if strings.Contains(b.Text, "history cleared") {
			noticed = true
		}
	}
	if !noticed {
		t.Fatal("idle /clear must show the 'history cleared' notice")
	}
	// The cleared conversation must be recoverable (SaveAfterTurn intent).
	if sessions, _ := sess.ListSessions(); len(sessions) == 0 {
		t.Fatal("idle /clear did not persist the old conversation before clearing")
	}
}
