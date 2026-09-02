package conversation

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
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
	events       chan uievent.Event
	activeHandle ports.TurnHandle
	history      []ports.Message
	sent         []string
}

func (c *scriptedThread) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.sent = append(c.sent, in.Text)
	h := scriptedHandle{ch: c.events}
	c.activeHandle = h
	return h, nil
}
func (c *scriptedThread) History() []ports.Message { return c.history }
func (c *scriptedThread) ActiveTurn() (ports.TurnHandle, bool) {
	if c.activeHandle != nil {
		return c.activeHandle, true
	}
	return nil, false
}
func (c *scriptedThread) Model() ports.ModelInfo    { return ports.ModelInfo{} }
func (c *scriptedThread) ContextUsage() ports.Usage { return ports.Usage{} }
func (c *scriptedThread) Title() string             { return "scripted thread" }
func (c *scriptedThread) ID() string                { return "scripted thread" }

// stubThreads is the ports.SubagentThreads seam for tests.
type stubThreads map[string]ports.Conversation

func (t stubThreads) Thread(callID string) (ports.Conversation, bool) {
	c, ok := t[callID]
	return c, ok
}

// CancelSubagentTask is not exercised by these tests (they cover thread
// rendering, not cancellation); it reports a clean miss.
func (t stubThreads) CancelSubagentTask(string) (bool, error) { return false, nil }

// CancelSubagentToolCall mirrors CancelSubagentTask's clean-miss stub.
func (t stubThreads) CancelSubagentToolCall(string, string) (bool, error) { return false, nil }

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
// subagent's own ports.Conversation - and that it is strictly
// read-only: no composer, no key routes into it or sends anything. A
// live subagent's stream still renders through the shared
// handleTurnEvent path into the thread's own transcript, picked up
// automatically from the conversation's ActiveTurn rather than from
// anything the operator typed (see openThread).
func TestSubagentThreadDialogReusesConversationScreen(t *testing.T) {
	thread := &scriptedThread{
		events: make(chan uievent.Event, 8),
		history: []ports.Message{
			{Role: "user", Text: "scout the config constants"},
			{Role: "assistant", Text: "I read defaults.go end to end."},
		},
	}
	thread.activeHandle = scriptedHandle{ch: thread.events}
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
	if s.thread.active == nil {
		t.Fatal("opening a live subagent's dialog did not pick up its ActiveTurn")
	}

	assertThreadDialogIsReadOnly(t, s, thread)
}

func assertThreadDialogIsReadOnly(t *testing.T, s Screen, thread *scriptedThread) {
	t.Helper()
	if !s.thread.hideComposer {
		t.Fatal("a subagent thread dialog must always hide its composer")
	}
	dialog := s.View()
	stripped := ansi.Strip(dialog)
	if !strings.Contains(stripped, "scout the config constants") || !strings.Contains(stripped, "esc close") {
		t.Errorf("thread dialog missing its transcript or close hint:\n%s", dialog)
	}
	if tail := s.thread.chatTailRows(); len(tail) != 1 {
		t.Errorf("expected 1 chat tail row (status line only, no composer), got %d rows: %v", len(tail), tail)
	}

	// Typing must not reach the thread's composer, the main composer, or
	// send anything: the operator has no channel into a subagent's own
	// conversation, running or finished.
	s = typeText(t, s, "go deeper")
	if got := s.thread.composer.Value(); got != "" {
		t.Fatalf("typing reached the read-only thread composer: %q", got)
	}
	if got := s.composer.Value(); got != "" {
		t.Fatalf("typed text leaked into the main composer: %q", got)
	}
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(thread.sent) != 0 {
		t.Fatalf("enter sent a message into a read-only subagent thread: %v", thread.sent)
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
	if !strings.Contains(plain, "esc:close dialog") {
		t.Errorf("embedded view status row missing \"esc:close dialog\":\n%s", plain)
	}
	if strings.Contains(plain, "ctrl+c:quit") {
		t.Errorf("embedded view status row must NOT show \"ctrl+c:quit\":\n%s", plain)
	}
}

func TestSubagentThreadCtrlCClosesDialogWithoutQuitting(t *testing.T) {
	s := threadScreen(t, stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}}, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if !s.panel.dialog || s.panel.dialogAgent != "sa-1" {
		t.Fatal("enter did not open the thread dialog")
	}

	// Press ctrl+c inside the open thread dialog
	next, cmd := s.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	s = next.(Screen)

	// Dialog must be closed
	if s.panel.dialog || s.panel.dialogAgent != "" {
		t.Fatal("ctrl+c did not close the thread dialog")
	}
	// Program quit must NOT be triggered
	if s.quitArmed {
		t.Fatal("ctrl+c inside thread dialog armed quit, want dialog closed only")
	}
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("ctrl+c inside thread dialog issued tea.Quit")
		}
	}
}

// TestSubagentThreadDialogIsReadOnlyForSlashCommands: a subagent
// dialog's composer is always hidden (see openThread), so keys that
// would type a slash command must be swallowed rather than routed to a
// command runner - there is no "thread command" affordance left to
// exercise.
func TestSubagentThreadDialogIsReadOnlyForSlashCommands(t *testing.T) {
	runner := &fakeRunner{outcome: ports.CommandOutcome{Notice: "thread command ran"}}
	s := threadScreen(t, stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}}, false)
	s.SetCommands([]composer.Command{{Name: "help", Desc: "show help"}})
	s.SetCommandRunner(runner)

	// Enter opens the thread dialog
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil {
		t.Fatal("thread is nil after opening dialog")
	}

	// Typing /help must not reach the (hidden) thread composer.
	for _, ch := range "/help" {
		next, _ = s.Update(tea.KeyPressMsg{Text: string(ch)})
		s = next.(Screen)
	}
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	if len(runner.calls) != 0 {
		t.Errorf("runner calls = %v, want none: a read-only subagent dialog must not run commands", runner.calls)
	}
	if got := s.thread.composer.Value(); got != "" {
		t.Errorf("thread composer kept %q; a read-only dialog must never accept input", got)
	}
}

// TestThemeChangeReachesTheCachedThreadScreen: openThread reuses the
// cached embedded Screen for the same call ID, so a thread opened
// before a theme switch and reopened after it must come back in the new
// theme, not the one it was built with.
func TestThemeChangeReachesTheCachedThreadScreen(t *testing.T) {
	_, light, _ := themePair(t)
	thread := &scriptedThread{history: []ports.Message{{Role: "user", Text: "scout"}}}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil {
		t.Fatal("the thread dialog did not build an embedded Screen")
	}

	next, _ = s.Update(app.ThemeChangedMsg{Theme: light, Tier: theme.TierTrueColor})
	s = next.(Screen)

	if s.thread == nil {
		t.Fatal("the theme change dropped the cached thread")
	}
	if got := s.thread.Theme.Name; got != light.Name {
		t.Errorf("cached thread screen kept theme %q, want %q", got, light.Name)
	}
	for name, th := range map[string]theme.Theme{
		"composer":   s.thread.composer.Theme,
		"transcript": s.thread.transcript.Theme,
		"statusline": s.thread.statusline.Theme,
		"approval":   s.thread.approval.Theme,
		"topbar":     s.thread.topbar.Theme,
		"welcome":    s.thread.welcome.Theme,
		"panel.list": s.thread.panel.list.Theme,
	} {
		if th.Name != light.Name {
			t.Errorf("cached thread's %s kept theme %q, want %q", name, th.Name, light.Name)
		}
	}
	if s.thread.Tier != theme.TierTrueColor {
		t.Errorf("cached thread screen kept tier %v, want TierTrueColor", s.thread.Tier)
	}
}

// TestEmbeddedThreadSwallowsF2 pins the screen-stack-globals swallow
// list: an embedded subagent-thread construction owns no terminal
// surface of its own, so f2 (which opens settings on the MAIN screen)
// must be swallowed rather than pushing a second settings screen from
// inside the thread dialog.
func TestEmbeddedThreadSwallowsF2(t *testing.T) {
	conv := &scriptedThread{events: make(chan uievent.Event, 4)}
	s := NewThread(loadTheme(t), theme.TierASCII, conv, 60, fixedNow)
	s.setSurface(60, 12)
	_, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyF2})
	if cmd != nil {
		t.Error("expected f2 to be swallowed inside an embedded thread")
	}
}

func TestLoadHistory_RendersToolCallsAndReasoning(t *testing.T) {
	conv := &scriptedThread{
		history: []ports.Message{
			{Role: "user", Text: "read file"},
			{
				Role:      "assistant",
				Text:      "Done reading file",
				Reasoning: "Looking up file contents",
				ToolCalls: []ports.ToolCall{
					{
						ID:        "tc-1",
						Name:      "view_file",
						Arguments: `{"path": "foo.go"}`,
						Output:    "package foo",
					},
				},
			},
		},
	}
	s := New(loadTheme(t), theme.TierASCII, nil, conv, nil, 80, fixedNow)
	s.LoadHistory(conv.History())
	view := ansi.Strip(s.transcript.View())
	for _, want := range []string{"read file", "view_file", "Done reading file"} {
		if !strings.Contains(view, want) {
			t.Errorf("transcript view missing %q:\n%s", want, view)
		}
	}
}

func TestLoadHistory_HydratesSubagentsWithCompletedStatus(t *testing.T) {
	conv := &scriptedThread{
		history: []ports.Message{
			{Role: "user", Text: "run subagent"},
			{
				Role: "assistant",
				Text: "Task complete",
				ToolCalls: []ports.ToolCall{
					{
						ID:        "sa-1",
						Name:      "invoke_subagent",
						Arguments: `{"name": "worker"}`,
						Output:    `{"done": true}`,
					},
				},
			},
		},
	}
	s := New(loadTheme(t), theme.TierASCII, nil, conv, nil, 80, fixedNow)
	s.LoadHistory(conv.History())
	if len(s.panel.agents) != 1 {
		t.Fatalf("expected 1 subagent hydrated in panel, got %d", len(s.panel.agents))
	}
	if s.panel.agents[0].ID != "sa-1" || s.panel.agents[0].Status != "completed" {
		t.Errorf("got subagent %+v, want ID='sa-1', Status='completed'", s.panel.agents[0])
	}
}

func TestLoadHistory_HydratesTrailingEmptySubagentAsInterrupted(t *testing.T) {
	conv := &scriptedThread{
		history: []ports.Message{
			{Role: "user", Text: "start subagent"},
			{
				Role: "assistant",
				Text: "", // empty text, no output -> harness was interrupted mid-execution
				ToolCalls: []ports.ToolCall{
					{
						ID:        "sa-crash",
						Name:      "invoke_subagent",
						Arguments: `{"name": "worker"}`,
						Output:    "",
					},
				},
			},
		},
	}
	s := New(loadTheme(t), theme.TierASCII, nil, conv, nil, 80, fixedNow)
	s.LoadHistory(conv.History())
	if len(s.panel.agents) != 1 {
		t.Fatalf("expected 1 subagent hydrated in panel, got %d", len(s.panel.agents))
	}
	if s.panel.agents[0].ID != "sa-crash" || s.panel.agents[0].Status != "interrupted" {
		t.Errorf("got subagent %+v, want ID='sa-crash', Status='interrupted'", s.panel.agents[0])
	}
}

func TestSubagentThreadOpensWithoutBackdrop(t *testing.T) {
	thread := &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: []ports.Message{{Role: "assistant", Text: "subagent ready"}},
	}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	// Enter on the subagent row opens the subagent thread view
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	if !s.panel.dialog || s.panel.dialogAgent != "sa-1" {
		t.Fatalf("expected subagent dialog open, got dialog=%v agent=%q", s.panel.dialog, s.panel.dialogAgent)
	}

	view := s.View()
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		t.Fatal("empty view returned")
	}

	// In wide mode (split), the first content row in the reading pane must start with the top-left border corner "╭"
	// with no margin padding gaps before it.
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "╭") {
		t.Errorf("expected view to contain top-left border corner without backdrop:\n%s", plain)
	}
	if !strings.Contains(plain, "subagent: sa-1") {
		t.Errorf("expected view to contain subagent title:\n%s", plain)
	}
	if !strings.Contains(plain, "subagent ready") {
		t.Errorf("expected view to contain subagent thread content:\n%s", plain)
	}
}

func TestThreadDialog_ScrollingAndHistory(t *testing.T) {
	thread := &scriptedThread{
		events: make(chan uievent.Event, 4),
		history: []ports.Message{
			{Role: "user", Text: "deep thought query"},
			{Role: "assistant", Text: "first paragraph of reasoning", Reasoning: "step-by-step thinking", ToolCalls: []ports.ToolCall{
				{ID: "tc-1", Name: "grep", Arguments: `{"query":"foo"}`, Output: "bar"},
			}},
		},
	}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	// Open dialog
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// PgUp / PgDown / Home / End / ctrl+u / ctrl+d / Up / Down scrolling
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	s = next.(Screen)
	next, _ = s.Update(keyMsg("ctrl+u"))
	s = next.(Screen)
	next, _ = s.Update(keyMsg("ctrl+d"))
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	if s.thread == nil {
		t.Fatal("thread must remain open after scroll keys")
	}
}

// TestThreadDialog_HomeEndAlwaysScrollsTranscript: a subagent dialog's
// composer is always hidden and never has text to protect a cursor
// for, so Home/End always scroll the transcript - unlike the main
// screen's composer, which still needs the Home/End-moves-cursor
// carve-out.
func TestThreadDialog_HomeEndAlwaysScrollsTranscript(t *testing.T) {
	thread := &scriptedThread{
		events:  make(chan uievent.Event, 4),
		history: []ports.Message{{Role: "assistant", Text: "ready"}},
	}
	s := threadScreen(t, stubThreads{"sa-1": thread}, false)

	// Open subagent dialog
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Typing must not reach the hidden thread composer.
	s = typeText(t, s, "hello world")
	if got := s.thread.composer.Value(); got != "" {
		t.Fatalf("expected the read-only thread composer to stay empty, got %q", got)
	}

	// Home key scrolls the transcript rather than moving a (nonexistent) cursor.
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	s = next.(Screen)
	if got := s.thread.composer.Value(); got != "" {
		t.Errorf("expected composer to remain empty after Home, got %q", got)
	}
}

func TestResumedSession_SubagentHistoryAvailableInDialog(t *testing.T) {
	subagentConv := &scriptedThread{
		events: make(chan uievent.Event, 4),
		history: []ports.Message{
			{Role: "user", Text: "perform detailed research on memory leaks"},
			{Role: "assistant", Text: "found 0 leaks across 12 packages"},
		},
	}
	// Namespaced with the tool call's own id, matching what LoadHistory
	// actually keys the reconstructed panel row and thread lookup under
	// (see thread.go's namespacedTaskID call) - internal/cliorchestrate/
	// dispatch.go's dispatchNamespace is the same scheme a live dispatch
	// mints its real per-task id under.
	threads := stubThreads{"call_disp_99:task-leak-check": subagentConv}

	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 30})
	scr := next.(Screen)
	scr.SetSubagentThreads(threads)

	// Simulate resuming a session: LoadHistory with a dispatch_tasks call
	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_disp_99",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"id":"task-leak-check","prompt":"perform detailed research on memory leaks","agent":"researcher"}]}`,
					Output:    `{"tasks":[{"id":"task-leak-check","status":"completed","output":"found 0 leaks across 12 packages"}]}`,
				},
			},
		},
	}
	scr.LoadHistory(msgs)

	// Open sidebar panel
	scr = openPanel(t, scr)

	// Focus is on the subagent row in the panel
	a, isAgent := scr.panel.selectedAgent()
	if !isAgent || a.ID != "call_disp_99:task-leak-check" {
		t.Fatalf("expected selected subagent 'call_disp_99:task-leak-check', got isAgent=%v, id=%s", isAgent, a.ID)
	}

	// Press Enter to open the subagent dialog
	next, _ = scr.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	scr = next.(Screen)

	if scr.thread == nil {
		t.Fatal("expected subagent thread dialog to open")
	}
	if scr.panel.dialogAgent != "call_disp_99:task-leak-check" {
		t.Errorf("expected dialogAgent='call_disp_99:task-leak-check', got %q", scr.panel.dialogAgent)
	}

	// Verify history is loaded in the thread dialog
	rendered := scr.thread.transcript.View()
	if !strings.Contains(rendered, "perform detailed research") && !strings.Contains(rendered, "found 0 leaks") {
		t.Errorf("expected rendered thread transcript to contain subagent history, got:\n%s", rendered)
	}

	// When viewing subagent history, composer must be hidden.
	if !scr.thread.hideComposer {
		t.Errorf("expected hideComposer=true when viewing subagent history in dialog, got false")
	}
	tailRows := scr.thread.chatTailRows()
	if len(tailRows) != 1 {
		t.Errorf("expected 1 chat tail row (status line only) when composer is hidden, got %d rows: %v", len(tailRows), tailRows)
	}
}

// TestLoadHistory_DispatchTasksMissingIDFallbackIsFriendly pins the fix
// for a raw provider tool_call_id leaking into a visible sidebar row on a
// RESUMED session: a task the model forgot to give an "id" used to fall
// back to "{callID}-{index}", so LoadHistory's reconstructed row exposed
// the raw call id verbatim, e.g. "call_disp_99-1". A resumed session must
// key its row the same friendly way a live dispatch_tasks run now does
// (dispatchTaskIDs in events.go).
func TestLoadHistory_DispatchTasksMissingIDFallbackIsFriendly(t *testing.T) {
	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 30})
	scr := next.(Screen)

	msgs := []ports.Message{
		{
			Role: "assistant",
			At:   time.Now(),
			ToolCalls: []ports.ToolCall{
				{
					ID:        "call_disp_99",
					Name:      "dispatch_tasks",
					Arguments: `{"tasks":[{"prompt":"tidy the docs","agent":"docs-writer"}]}`,
					Output:    `{"tasks":[{"status":"completed","output":"done"}]}`,
				},
			},
		},
	}
	scr.LoadHistory(msgs)

	for _, a := range scr.panel.agents {
		if strings.Contains(a.ID, "call_disp_99") {
			t.Fatalf("row id %q must not embed the raw provider call id", a.ID)
		}
	}
	found := false
	for _, a := range scr.panel.agents {
		if a.ID == "task-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a friendly fallback row id (task-1), got %+v", scr.panel.agents)
	}
}

func TestSubagentHistoryDialog_AlwaysHidesComposer(t *testing.T) {
	subCompleted := &scriptedThread{
		events: make(chan uievent.Event, 4),
		history: []ports.Message{
			{Role: "user", Text: "audit dependencies"},
			{Role: "assistant", Text: "all dependencies up to date"},
		},
	}
	subRunning := &scriptedThread{
		events: make(chan uievent.Event, 4),
		history: []ports.Message{
			{Role: "user", Text: "run security scan"},
		},
	}
	threads := stubThreads{
		"sa-done": subCompleted,
		"sa-live": subRunning,
	}

	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 30})
	scr := next.(Screen)
	scr.SetSubagentThreads(threads)

	// Register sa-done as completed (history) and sa-live as running (active)
	scr.panel.observeAgentHistory("sa-done", "completed")
	n, _ := scr.Update(agentEvent("sa-live", "running", 1, 2, "scanning"))
	scr = n.(Screen)

	// 1. Verify main conversation screen retains composer
	if scr.hideComposer {
		t.Errorf("main conversation screen must not have hideComposer=true")
	}
	mainTail := scr.chatTailRows()
	if len(mainTail) < 2 {
		t.Errorf("expected main conversation tail rows to include composer and status row, got %d rows: %v", len(mainTail), mainTail)
	}

	// 2. Open sidebar and select sa-done (history)
	scr = openPanel(t, scr)
	scr.panel.list.MoveTo(0) // sa-done is first row
	next, _ = scr.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	scr = next.(Screen)

	if scr.thread == nil || scr.panel.dialogAgent != "sa-done" {
		t.Fatalf("expected sa-done thread dialog open, got dialogAgent=%q", scr.panel.dialogAgent)
	}
	if !scr.thread.hideComposer {
		t.Errorf("expected hideComposer=true for completed subagent history dialog, got false")
	}
	doneTail := scr.thread.chatTailRows()
	if len(doneTail) != 1 {
		t.Errorf("expected 1 chat tail row (status line only) for completed subagent, got %d rows: %v", len(doneTail), doneTail)
	}

	// Esc to close dialog back to list
	next, _ = scr.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	scr = next.(Screen)

	// 3. Select sa-live (running subagent) - the composer stays hidden
	// even though the subagent has not reached a terminal status: the
	// operator has no real channel to it either way (see openThread).
	scr.panel.list.MoveTo(1) // sa-live is second row
	next, _ = scr.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	scr = next.(Screen)

	if scr.thread == nil || scr.panel.dialogAgent != "sa-live" {
		t.Fatalf("expected sa-live thread dialog open, got dialogAgent=%q", scr.panel.dialogAgent)
	}
	if !scr.thread.hideComposer {
		t.Errorf("expected hideComposer=true for running subagent dialog, got false")
	}
	liveTail := scr.thread.chatTailRows()
	if len(liveTail) != 1 {
		t.Errorf("expected 1 chat tail row (status line only) for running subagent, got %d rows: %v", len(liveTail), liveTail)
	}
}

func TestSubagentHistoryDialog_LiveCompletionHidesComposer(t *testing.T) {
	subRunning := &scriptedThread{
		events: make(chan uievent.Event, 4),
		history: []ports.Message{
			{Role: "user", Text: "build binary"},
		},
	}
	threads := stubThreads{"sa-task": subRunning}

	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 30})
	scr := next.(Screen)
	scr.SetSubagentThreads(threads)

	// Subagent starts running
	n, _ := scr.Update(agentEvent("sa-task", "running", 1, 3, "compiling"))
	scr = n.(Screen)

	// Open subagent dialog while running: still read-only.
	scr = openPanel(t, scr)
	next, _ = scr.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	scr = next.(Screen)

	if !scr.thread.hideComposer {
		t.Fatal("expected hideComposer=true while subagent is running")
	}

	// Subagent tool call ends (completed)
	n, _ = scr.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{
			ToolCallID: "sa-task",
			Name:       "invoke_subagent",
			OK:         true,
			Result:     `{"status":"completed"}`,
		},
	}})
	scr = n.(Screen)

	// Reopen the cached thread directly
	ok, _ := scr.openThread("sa-task")
	if !ok || !scr.thread.hideComposer {
		t.Errorf("expected reopening cached completed thread to keep hideComposer=true, got ok=%v, hideComposer=%v", ok, scr.thread.hideComposer)
	}
}
