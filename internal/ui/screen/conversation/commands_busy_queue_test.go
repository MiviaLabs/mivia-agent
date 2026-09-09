package conversation

// Split out of commands_test.go to keep it under the file-size hard cap.

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

// TestRunSlashCommandSubmitPromptQueuesWhileBusy pins the s.active != nil
// branch: a SubmitPrompt outcome arriving while a turn is already active
// must queue the text and update the statusline's queued count, rather
// than sending immediately.
func TestRunSlashCommandSubmitPromptQueuesWhileBusy(t *testing.T) {
	conv := &capturingConversation{Conversation: replay.New(nil, 0)}
	runner := &fakeRunner{outcome: ports.CommandOutcome{SubmitPrompt: "queued prompt"}}
	s := newScreen(t, conv, nil, nil)
	s.SetCommandRunner(runner)
	s.active = &fakeTurnHandle{}

	next, _ := s.runSlashCommand("/busy")
	scr := next.(Screen)

	if len(scr.queue) != 1 || scr.queue[0] != "queued prompt" {
		t.Fatalf("queue = %v, want [\"queued prompt\"]", scr.queue)
	}
	if conv.lastSend.Text != "" {
		t.Fatalf("Send was called while busy: %+v", conv.lastSend)
	}
}

// TestClearTranscriptOutcome_EmptyTitleClearsBreadcrumb pins the
// else-branch's own title == "" case: clearing the transcript in place
// (no replacement Conversation) against a conv whose Title() is empty
// must clear the breadcrumb rather than leaving a stale one.
func TestClearTranscriptOutcome_EmptyTitleClearsBreadcrumb(t *testing.T) {
	conv := &fakeCustomConversation{title: ""}
	runner := &fakeRunner{outcome: ports.CommandOutcome{ClearTranscript: true}}
	s := newScreen(t, conv, nil, nil)
	s.SetCommandRunner(runner)
	s.topbar.SetBreadcrumb([]string{"stale title"})

	s, _ = sendLine(t, s, "/clear")

	if strings.Contains(s.topbar.View(), "stale title") {
		t.Fatal("topbar still shows the stale breadcrumb after /clear against an empty-title conversation")
	}
}
