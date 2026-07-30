package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// forceSendModel builds a chat model mid-turn with a queued follow-up, ready to
// exercise the empty-Enter force-send path (tui_keys.go handleChatEnter).
func forceSendModel(t *testing.T, streamed string) *tuiModel {
	t.Helper()
	m := newSmokeModel(t)
	m.mode = modeChat
	// A stub completer: force-send really starts the queued turn, so the worker
	// goroutine must have something to talk to.
	m.session = &chat.Session{Model: "test-model", Completer: welcomeStubCompleter{}}
	m.waiting = true
	m.turnStart = time.Now()
	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "first question"})
	// The model already streamed a partial answer into the live buffer.
	m.streamBuf.WriteString(streamed)
	m.pendingQueue = []string{"second question"}
	m.layout()
	m.renderVP()
	return m
}

// TestCancelKeepsPartialAnswerOnForceSend locks the defect where superseding a
// running turn threw its answer away. startAI is reachable while m.waiting via
// empty-Enter force-send, and it reset streamBuf/thinkingBuf/toolRows and swapped
// the bridge without committing anything - so the previous turn's visible answer
// vanished and two user blocks appeared back to back. The Ctrl+C path already
// committed correctly; this extends that guarantee to any superseded turn.
func TestCancelKeepsPartialAnswerOnForceSend(t *testing.T) {
	const partial = "Both fixes work. Here is the proof:"
	m := forceSendModel(t, partial)

	// Empty composer + queued item is the real force-send path.
	m.textarea.Reset()
	_, _, _ = m.handleChatEnter(false)

	if !hasAssistantText(m.blocks, partial) {
		t.Fatalf("force-send discarded the in-flight answer; blocks: %v", blockTexts(m.blocks))
	}
	if m.streamBuf.Len() != 0 {
		t.Fatalf("stream buffer should have been committed, still holds %q", m.streamBuf.String())
	}

	// The superseded turn must be closed before the new one opens: no two
	// consecutive user blocks with nothing between them.
	var lastUser = -1
	for i, b := range m.blocks {
		if b.Kind != ChatBlockUser {
			continue
		}
		if lastUser >= 0 && i == lastUser+1 {
			t.Fatalf("two adjacent user blocks - superseded turn was never closed; blocks: %v", blockTexts(m.blocks))
		}
		lastUser = i
	}

	// And the new turn actually started.
	if !m.waiting {
		t.Fatal("force-send must start the queued turn")
	}
	foundNew := false
	for _, b := range m.blocks {
		if b.Kind == ChatBlockUser && strings.Contains(b.Text, "second question") {
			foundNew = true
		}
	}
	if !foundNew {
		t.Fatalf("queued message was not sent; blocks: %v", blockTexts(m.blocks))
	}
}
