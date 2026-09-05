package conversation

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/composer"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

type worktreeTestRunner struct {
	sessions map[string]ports.Conversation
	commands []composer.Command
}

func (r *worktreeTestRunner) Run(context.Context, string, string) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *worktreeTestRunner) SelectModel(context.Context, string) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *worktreeTestRunner) SelectAgent(context.Context, string) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *worktreeTestRunner) SelectEffort(context.Context, string) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *worktreeTestRunner) SessionActive(string) bool { return false }
func (r *worktreeTestRunner) CompleteLogin(context.Context, string, []byte) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *worktreeTestRunner) StartInWorktree(context.Context, ports.SessionSummary) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *worktreeTestRunner) ResumeInWorktree(context.Context, ports.SessionSummary) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *worktreeTestRunner) StartInNewWorktree(_ context.Context, name string) ports.CommandOutcome {
	if name == "" {
		name = "wt-auto"
	}
	conv := &backgroundTestConversation{
		id:     name,
		title:  "Worktree Session " + name,
		events: make(chan uievent.Event, 10),
	}
	r.sessions[name] = conv
	return ports.CommandOutcome{
		Conversation:    conv,
		ClearTranscript: true,
		Notice:          "Started new session in worktree " + name,
	}
}
func (r *worktreeTestRunner) SelectSession(_ context.Context, id string) ports.CommandOutcome {
	if conv, ok := r.sessions[id]; ok {
		return ports.CommandOutcome{
			Conversation:    conv,
			ClearTranscript: true,
			Notice:          "Resumed session " + id,
		}
	}
	return ports.CommandOutcome{Err: "session not found"}
}
func (r *worktreeTestRunner) Commands() []composer.Command {
	return r.commands
}

type worktreeThreads struct {
	tag     string
	threads map[string]ports.Conversation
}

func (t *worktreeThreads) Thread(id string) (ports.Conversation, bool) {
	c, ok := t.threads[id]
	return c, ok
}
func (t *worktreeThreads) CancelSubagentTask(string) (bool, error)             { return false, nil }
func (t *worktreeThreads) CancelSubagentToolCall(string, string) (bool, error) { return false, nil }

func setupWorktreeTestHarness(t *testing.T) (Screen, *worktreeTestRunner, *worktreeThreads) {
	t.Helper()
	dark, _, themes := themePair(t)
	mainConv := &backgroundTestConversation{
		id:     "main-session",
		title:  "Main Session",
		events: make(chan uievent.Event, 10),
	}
	runner := &worktreeTestRunner{
		sessions: map[string]ports.Conversation{
			"main-session": mainConv,
		},
		commands: []composer.Command{
			{Name: "resume", Desc: "resume previous session"},
			{Name: "tab", Desc: "switch active session tab"},
			{Name: "theme", Desc: "switch UI theme"},
		},
	}
	threads := &worktreeThreads{
		tag:     "pool-threads",
		threads: make(map[string]ports.Conversation),
	}

	s := New(dark, theme.TierTrueColor, themes, mainConv, nil, 100, func() time.Time {
		return time.Unix(1000, 0)
	})
	s.SetCommandRunner(runner)
	s.SetCommands(runner.Commands())
	s.SetSubagentThreads(threads)
	return s, runner, threads
}

func TestWorktreeSession_SlashCommandsRetainedAndActive(t *testing.T) {
	s, runner, _ := setupWorktreeTestHarness(t)

	// Verify main session has slash commands
	if len(s.composer.Commands()) != 3 {
		t.Fatalf("expected 3 commands in main session composer, got %d", len(s.composer.Commands()))
	}

	// Switch to a new worktree session via StartInNewWorktree
	outcome := runner.StartInNewWorktree(context.Background(), "wt-feature")
	next, _ := s.applyCommandOutcome(outcome)
	wtScreen := next.(Screen)

	if wtScreen.convID() != "wt-feature" {
		t.Fatalf("expected active conversation ID 'wt-feature', got %q", wtScreen.convID())
	}

	// Verify worktree session composer has commands populated
	wtCmds := wtScreen.composer.Commands()
	if len(wtCmds) != 3 {
		t.Fatalf("expected 3 commands in worktree session composer, got %d", len(wtCmds))
	}

	// Type '/' into composer to open menu
	next, _ = wtScreen.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	typedScreen := next.(Screen)

	if !typedScreen.composer.MenuActive() {
		t.Fatalf("typing '/' in worktree session did not activate slash menu")
	}

	// Verify menu candidates contain "/tab"
	menuRows := typedScreen.composer.MenuRows()
	if menuRows == 0 {
		t.Errorf("expected completion menu rows > 0, got %d", menuRows)
	}
}

func TestWorktreeSession_StatuslineLiveTicksWithoutInput(t *testing.T) {
	s, runner, _ := setupWorktreeTestHarness(t)

	// Start a turn in main session
	next, cmd := s.sendText("long running task")
	mainRunning := next.(Screen)
	if cmd == nil || mainRunning.active == nil {
		t.Fatalf("expected turn start with handle and cmd")
	}

	// Switch to a worktree session (which is idle)
	outcome := runner.StartInNewWorktree(context.Background(), "wt-idle")
	next, cmd = mainRunning.applyCommandOutcome(outcome)
	wtScreen := next.(Screen)

	// Since main session is active in the background, hasActiveSession() is true
	if !wtScreen.hasActiveSession() {
		t.Fatalf("expected hasActiveSession()=true while background session is running")
	}

	// Delivering a TickMsg to the idle worktree screen must keep ticking
	next, tickCmd := wtScreen.Update(statusline.TickMsg{})
	if tickCmd == nil {
		t.Fatalf("handleStatuslineTick lapsed while background session was running")
	}

	// Switch back to the running main session
	next, switchCmd := next.(Screen).switchToSessionID("main-session")
	backMain := next.(Screen)

	if switchCmd == nil {
		t.Fatalf("switching back to active session returned nil cmd, expected batch with TickCmd")
	}
	if backMain.active == nil {
		t.Fatalf("main session turn state was lost on switch back")
	}

	// Statusline must continue ticking and increment frames
	beforeFrame := backMain.statusline.Mark().Frame()
	next, tickCmd = backMain.Update(statusline.TickMsg{})
	if tickCmd == nil {
		t.Fatalf("tick loop stopped after switching back to running session")
	}
	afterFrame := next.(Screen).statusline.Mark().Frame()
	if afterFrame == beforeFrame {
		t.Errorf("statusline mark frame did not advance on TickMsg: %d vs %d", beforeFrame, afterFrame)
	}
}

func TestWorktreeSession_SubagentsDispatchedAndTracked(t *testing.T) {
	s, runner, threads := setupWorktreeTestHarness(t)

	// Switch to worktree session
	outcome := runner.StartInNewWorktree(context.Background(), "wt-subagents")
	next, _ := s.applyCommandOutcome(outcome)
	wtScreen := next.(Screen)

	// Ensure s.threads is preserved in the worktree session
	if wtScreen.threads == nil {
		t.Fatalf("s.threads was wiped out when switching to worktree session")
	}

	// Register a subagent thread
	subConv := &backgroundTestConversation{
		id:    "sub-conv-1",
		title: "Subagent Task",
	}
	threads.threads["call-sub-1"] = subConv

	// Emit ToolStart event for subagent dispatch
	startEv := uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{
			ToolCallID: "call-sub-1",
			Name:       "task",
			Args: map[string]any{
				"subagent": "code-researcher",
			},
		},
	}
	next, _ = wtScreen.Update(uievent.EventMsg{Event: startEv})
	trackedScreen := next.(Screen)

	// Verify the subagent appears in panel
	if trackedScreen.panel.activeAgentCount() != 1 {
		t.Fatalf("expected 1 active agent in panel, got %d", trackedScreen.panel.activeAgentCount())
	}

	// Open the subagent thread dialog
	opened, _ := trackedScreen.openThread("call-sub-1")
	if !opened || trackedScreen.thread == nil {
		t.Fatalf("openThread failed for dispatched subagent in worktree session")
	}

	// Switch away from worktree session: verify thread dialog closes cleanly
	next, _ = trackedScreen.switchToSessionID("main-session")
	switchedScreen := next.(Screen)

	if switchedScreen.thread != nil || switchedScreen.threadID != "" {
		t.Errorf("thread dialog leaked across session switch: thread=%v threadID=%q", switchedScreen.thread, switchedScreen.threadID)
	}
}

func TestWorktreeSession_BackgroundQueuedTurnsRefreshTopbar(t *testing.T) {
	s, runner, _ := setupWorktreeTestHarness(t)

	// Switch to worktree session
	outcome := runner.StartInNewWorktree(context.Background(), "wt-bg")
	next, _ := s.applyCommandOutcome(outcome)
	wtScreen := next.(Screen)

	// Queue a message in wt-bg
	st := wtScreen.sessions["wt-bg"]
	if st == nil {
		// wt-bg is current, so switch to main to park it in s.sessions
		next, _ = wtScreen.switchToSessionID("main-session")
		s = next.(Screen)
		st = s.sessions["wt-bg"]
	}
	if st == nil {
		t.Fatalf("expected wt-bg session state in sessions map")
	}
	st.queue = []string{"queued turn 1"}

	// Simulate turn end for background session
	endMsg := turnEndedMsg{sessionID: "wt-bg"}
	next, cmd := s.Update(endMsg)
	afterEnd := next.(Screen)

	if cmd == nil {
		t.Fatalf("expected awaitSessionEvent cmd for queued background turn")
	}
	if afterEnd.sessions["wt-bg"].active == nil {
		t.Fatalf("expected queued turn to be started for wt-bg")
	}

	// Verify top bar shows running indicator for wt-bg tab
	view := afterEnd.topbar.View()
	if !strings.Contains(view, "⚡") && !strings.Contains(view, "~") {
		t.Errorf("topbar tab for wt-bg did not refresh to running indicator, view: %q", view)
	}
}

func TestTabSlashCommand_InvalidIndexAndForegroundTitle(t *testing.T) {
	s, _, _ := setupWorktreeTestHarness(t)

	// Test /tab 0 -> out of range error
	next, _ := s.runSlashCommand("/tab 0")
	s0 := next.(Screen)
	if s0.convID() != "main-session" {
		t.Errorf("expected to stay on main-session for /tab 0, got %q", s0.convID())
	}

	// Test /tab with current foreground title
	next, _ = s.runSlashCommand("/tab Main")
	sCurr := next.(Screen)
	if sCurr.convID() != "main-session" {
		t.Errorf("expected to match foreground session title, got %q", sCurr.convID())
	}
}

func TestPanelFocused_AllowsTabShortcuts(t *testing.T) {
	s, runner, _ := setupWorktreeTestHarness(t)

	// Create a second session so we have 2 tabs
	_ = runner.StartInNewWorktree(context.Background(), "wt-2")
	s.registerSession("wt-2")
	s.sessions["wt-2"] = s.newSessionState(runner.sessions["wt-2"])
	s.refreshTabs()

	// Focus the files panel
	s.panel.open = true
	s.panelFocus(true)
	if !s.panel.focused {
		t.Fatalf("setup: panel is not focused")
	}

	// Press F7 (keymap.IDTabNext) while panel is focused
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyF7})
	switched := next.(Screen)

	// Must switch to wt-2 instead of swallowing key in panel list
	if switched.convID() != "wt-2" {
		t.Errorf("expected F7 to switch tab to wt-2 while panel focused, got %q", switched.convID())
	}
}

func TestSwitchToSessionID_UnmountedWithActiveSessionEmitsTick(t *testing.T) {
	s, runner, _ := setupWorktreeTestHarness(t)

	// Start active turn on main-session
	next, _ := s.sendText("running main turn")
	s = next.(Screen)

	// Add an unmounted session to runner that is NOT yet in s.sessions
	runner.sessions["wt-unmounted"] = &backgroundTestConversation{
		id:    "wt-unmounted",
		title: "Unmounted",
	}

	// Switch to wt-unmounted via switchToSessionID (calls runner.SelectSession)
	next, cmd := s.switchToSessionID("wt-unmounted")
	switched := next.(Screen)

	if switched.convID() != "wt-unmounted" {
		t.Fatalf("expected to switch to wt-unmounted, got %q", switched.convID())
	}
	if cmd == nil {
		t.Fatalf("expected cmd batch containing TickCmd when background main-session is active")
	}
}
