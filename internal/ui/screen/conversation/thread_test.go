package conversation

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// scriptedThread is a ports.Conversation the test drives by hand: Send
// records the text and returns a handle whose event channel the test
// owns, so the reply stream is fed event by event the way the running
// program's read continuation would deliver it.
type scriptedThread struct {
	events  chan uievent.Event
	history []ports.Message
	sent    []string
}

func (c *scriptedThread) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.sent = append(c.sent, in.Text)
	return scriptedHandle{ch: c.events}, nil
}
func (c *scriptedThread) History() []ports.Message  { return c.history }
func (c *scriptedThread) Model() ports.ModelInfo    { return ports.ModelInfo{} }
func (c *scriptedThread) ContextUsage() ports.Usage { return ports.Usage{} }
func (c *scriptedThread) Title() string             { return "scripted thread" }

// stubThreads is the ports.SubagentThreads seam for tests.
type stubThreads map[string]ports.Conversation

func (t stubThreads) Thread(callID string) (ports.Conversation, bool) {
	c, ok := t[callID]
	return c, ok
}

// agentEvent is one subagent progress observation.
func agentEvent(id, status string, step, total int, log ...string) uievent.EventMsg {
	return uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: id,
			Progress: &uievent.Progress{Step: step, TotalSteps: total, Status: status, Log: log}},
	}}
}

// threadScreen is a wide screen with the panel open, one subagent
// tracked, and the threads seam wired.
func threadScreen(t *testing.T, threads ports.SubagentThreads, withFile bool) Screen {
	t.Helper()
	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 30})
	scr := next.(Screen)
	if withFile {
		n, _ := scr.Update(diffEvent("c1", "a.go", 1, 0, "x"))
		scr = n.(Screen)
	}
	n, _ := scr.Update(agentEvent("sa-1", "running", 1, 3, "read defaults.go"))
	scr = n.(Screen)
	scr.SetSubagentThreads(threads)
	scr = openPanel(t, scr)
	return scr
}

// TestSubagentThreadDialogReusesConversationScreen is the
// centralisation proof. It asserts the TYPE-level facts - the dialog's
// thread IS a *conversation.Screen in embedded mode, wired to the
// subagent's own ports.Conversation - and then exercises the SAME
// send/turn-event path the main chat uses: typing lands in the thread's
// composer (not the main one), Enter runs Screen.send (a TurnHandle
// goes active), and the thread's streamed reply renders through
// handleTurnEvent into the thread's transcript without leaking into the
// main transcript.
func TestSubagentThreadDialogReusesConversationScreen(t *testing.T) {
	thread := &scriptedThread{
		events: make(chan uievent.Event, 8),
		history: []ports.Message{
			{Role: "user", Text: "scout the config constants"},
			{Role: "assistant", Text: "I read defaults.go end to end."},
		},
	}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	// Enter on the subagent row opens the thread dialog.
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.dialog || s.panel.dialogAgent != "sa-1" {
		t.Fatalf("enter did not open the subagent thread dialog: dialog=%v agent=%q", s.panel.dialog, s.panel.dialogAgent)
	}
	if s.thread == nil || !s.thread.embedded || s.threadID != "sa-1" {
		t.Fatalf("the thread dialog did not build an embedded conversation.Screen: %+v", s.thread)
	}
	if s.thread.conv != ports.Conversation(thread) {
		t.Fatal("the embedded screen is not wired to the subagent's own Conversation")
	}

	assertThreadDialogViewAndTyping(t, s, thread)
}

func assertThreadDialogViewAndTyping(t *testing.T, s Screen, thread *scriptedThread) {
	t.Helper()
	dialog := s.View()
	for _, want := range []string{"scout the config constants", "> ", "esc close"} {
		if !strings.Contains(ansi.Strip(dialog), want) {
			t.Errorf("thread dialog missing %q:\n%s", want, dialog)
		}
	}

	// Typing goes to the THREAD's composer, never the main one.
	s = typeText(t, s, "go deeper")
	if got := s.thread.composer.Value(); got != "go deeper" {
		t.Fatalf("typed text %q did not reach the thread composer", got)
	}
	if got := s.composer.Value(); got != "" {
		t.Fatalf("typed text leaked into the main composer: %q", got)
	}

	// Enter sends through the SAME Screen.send path: a TurnHandle goes
	// active on the embedded screen, the main screen's stays idle.
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEnter})
	if s.thread.active == nil {
		t.Fatal("enter did not run the shared send path on the thread screen")
	}
	if s.active != nil {
		t.Fatal("the thread's send armed the MAIN screen's turn")
	}
	if len(thread.sent) != 1 || thread.sent[0] != "go deeper" {
		t.Fatalf("the thread conversation saw %v, want [go deeper]", thread.sent)
	}

	assertThreadStreamingAndEnd(t, s)
}

func assertThreadStreamingAndEnd(t *testing.T, s Screen) {
	t.Helper()
	next, _ := s.Update(threadEventMsg{event: uievent.Event{
		Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "constants are thresholds"},
	}})
	s = next.(Screen)
	next, _ = s.Update(threadEventMsg{event: uievent.Event{
		Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "constants are thresholds"},
	}})
	s = next.(Screen)
	threadView := ansi.Strip(s.thread.View())
	if !strings.Contains(threadView, "constants are thresholds") {
		t.Errorf("the thread's reply did not render through the shared turn-event path:\n%s", threadView)
	}
	if strings.Contains(ansi.Strip(s.transcript.View()), "constants are thresholds") {
		t.Error("the thread's reply leaked into the MAIN transcript")
	}
	if got := s.thread.composer.Value(); got != "" {
		t.Errorf("thread composer kept %q after send", got)
	}

	next, _ = s.Update(threadEndedMsg{})
	s = next.(Screen)
	if s.thread.active != nil {
		t.Error("threadEndedMsg did not clear the thread's active handle")
	}
}

// TestSubagentThreadEscClosesWithoutLeaking: esc closes the thread
// dialog back to the list, the cached thread survives (its transcript
// IS the ongoing state), and typing afterwards goes to the MAIN
// composer.
func TestSubagentThreadEscClosesWithoutLeaking(t *testing.T) {
	s := threadScreen(t, stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}}, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	s = next.(Screen)
	if s.panel.dialog || s.panel.dialogAgent != "" {
		t.Fatal("esc did not close the thread dialog")
	}
	if !s.panel.open || !s.panel.focused {
		t.Fatal("esc must return to the open, focused list")
	}
	if s.thread == nil {
		t.Fatal("esc dropped the cached thread; reopening must continue it")
	}
	// Focus is back in the LIST (the dialog's esc returns to the list,
	// like the file dialog's): one more esc hands it to the composer,
	// and only then does typing reach the main chat.
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	s = next.(Screen)
	if s.panel.focused {
		t.Fatal("the second esc must hand focus to the composer")
	}
	next, _ = s.Update(keyMsg("b"))
	s = next.(Screen)
	if got := s.composer.Value(); got != "b" {
		t.Fatalf("typing after close went to %q, want the main composer", got)
	}
	if got := s.thread.composer.Value(); got != "" {
		t.Errorf("typing after close reached the thread composer: %q", got)
	}
}

// TestArrowsWalkSubagentsToo: the list's cursor covers every section -
// past the files onto the subagent rows, with the marker following.
func TestArrowsWalkSubagentsToo(t *testing.T) {
	s := threadScreen(t, nil, true)                         // one file, one subagent
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // file -> agent
	s = next.(Screen)
	if a, ok := s.panel.selectedAgent(); !ok || a.ID != "sa-1" {
		t.Fatalf("down did not walk onto the subagent row (selected %+v)", a)
	}
	view := s.View()
	if !strings.Contains(ansi.Strip(view), "> · sa-1") {
		t.Errorf("the subagent row does not carry the cursor marker:\n%s", view)
	}
}

// TestSubagentStepLogFallbackWhenNoThread: an entry with no resolvable
// thread still opens a dialog - the read-only step log the progress
// events carried - and any key closes it.
func TestSubagentStepLogFallbackWhenNoThread(t *testing.T) {
	s := threadScreen(t, nil, false) // no threads seam at all
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.dialog || s.panel.dialogAgent != "sa-1" || s.thread != nil {
		t.Fatalf("enter did not open the step-log fallback: dialog=%v agent=%q thread=%v",
			s.panel.dialog, s.panel.dialogAgent, s.thread)
	}
	view := ansi.Strip(s.View())
	if !strings.Contains(view, "read defaults.go") || !strings.Contains(view, "any key closes") {
		t.Errorf("step-log fallback missing its log or hint:\n%s", view)
	}
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	s = next.(Screen)
	if s.panel.dialog {
		t.Error("a key did not close the step-log dialog")
	}
}

// TestEmbeddedScreenViewShape: the embedded construction draws exactly
// its given surface - no top bar, composer and status row present -
// because the dialog frame it renders inside is the only chrome.
func TestEmbeddedScreenViewShape(t *testing.T) {
	conv := &scriptedThread{events: make(chan uievent.Event, 4)}
	s := NewThread(loadTheme(t), theme.TierASCII, conv, 60, fixedNow)
	s.setSurface(60, 12)
	view := s.View()
	rows := strings.Split(view, "\n")
	if len(rows) != 12 {
		t.Fatalf("embedded view is %d rows, want the 12 it was given", len(rows))
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w != 60 {
			t.Errorf("row %d width %d, want 60", i, w)
		}
	}
	plain := ansi.Strip(view)
	if strings.Contains(plain, "mivia") {
		t.Errorf("the embedded view draws a top bar:\n%s", plain)
	}
	if !strings.Contains(plain, "> ") {
		t.Errorf("the embedded view lost the composer:\n%s", plain)
	}
}
