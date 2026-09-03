package newtui

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/charmbracelet/x/ansi"
)

func TestBuildAppPropagatesThemeLoadError(t *testing.T) {
	original := loadThemes
	wantErr := errors.New("corrupt embedded theme")
	loadThemes = func() ([]theme.Theme, error) { return nil, wantErr }
	defer func() { loadThemes = original }()

	sess := chat.NewSession(&config.Resolved{}, nil)
	agentState := &cli.AgentSessionState{}
	if _, _, _, err := buildApp(sess, &config.Resolved{}, true, agentState, ""); !errors.Is(err, wantErr) {
		t.Fatalf("buildApp err = %v, want %v", err, wantErr)
	}
}

func TestBuildApp(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	res := &config.Resolved{}
	agentState := &cli.AgentSessionState{}

	appModel, _, _, err := buildApp(sess, res, true, agentState, "")
	if err != nil {
		t.Fatalf("buildApp failed: %v", err)
	}
	if appModel == nil {
		t.Fatal("expected non-nil app model")
	}
}

// TestRunTUICallsBuildApp exercises RunTUI's whole body - buildApp, the
// notifier wiring, the program run, and the lease-release defer - through
// the newTeaProgram seam. An earlier version ran the program on the
// process's real stdin and relied on Run() failing fast off-TTY, which
// holds on linux but not on windows, where it ran headless forever and
// died on the 10-minute test timeout (the same trap run_mouse_test.go
// fixed one commit earlier; this test hid behind that hang).
func TestRunTUICallsBuildApp(t *testing.T) {
	original := newTeaProgram
	newTeaProgram = func(root tea.Model) *tea.Program {
		p := tea.NewProgram(root, tea.WithInput(strings.NewReader("")), tea.WithOutput(io.Discard))
		// Quit blocks until the event loop is receiving, so firing it
		// before Run starts still lands exactly once the program is live.
		go p.Quit()
		return p
	}
	defer func() { newTeaProgram = original }()

	sess := chat.NewSession(&config.Resolved{}, nil)
	agentState := &cli.AgentSessionState{}

	done := make(chan error, 1)
	go func() { done <- RunTUI(sess, nil, true, agentState, "") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTUI = %v, want clean quit", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("RunTUI did not return after the program quit")
	}
}

func ctrl(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

// TestBuildApp_SubagentHistoryVisibleInDialog is an end-to-end smoke test
// of the production wiring path: it drives the real tea.Model exactly as a
// user would - open the panel, select the dispatched subagent row, open
// its dialog - starting from a session whose history already contains a
// completed dispatch_tasks call (the shape /resume hands back). The actual
// regression coverage for the wiring bug (a switched-to session's
// Conversation never getting SetSubagents called on it) lives at the
// uiadapter level: TestSessionPool_GetOrCreateWiresSubagentThreadsOnResume
// and TestSessionPool_CreateFreshWiresSubagentThreads, which fail against
// the pre-fix SessionPool and pass here only because buildApp now sources
// its Conversation and SubagentThreads from the SAME pool those exercise.
func TestBuildApp_SubagentHistoryVisibleInDialog(t *testing.T) {
	res := &config.Resolved{Model: "test-model"}
	sess := chat.NewSession(res, nil)
	sess.SessionID = "session-1"
	sess.Messages = []provider.Message{
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{
				{
					ID: "call_disp_1",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      "dispatch_tasks",
						Arguments: `{"tasks":[{"id":"task-leak-check","prompt":"perform detailed research on memory leaks","agent":"researcher"}]}`,
					},
				},
			},
		},
		{
			Role:       provider.RoleTool,
			ToolCallID: "call_disp_1",
			Content:    `[{"task_id":"task-leak-check","status":"completed","output":"found 0 leaks across 12 packages"}]`,
		},
	}
	agentState := &cli.AgentSessionState{}

	root, _, _, err := buildApp(sess, res, true, agentState, "")
	if err != nil {
		t.Fatalf("buildApp: %v", err)
	}

	m, _ := root.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m, _ = m.Update(ctrl('b')) // open the activity panel, focused on its list
	// The panel opens on its model row; the subagent is the next row.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "perform detailed research on memory leaks") {
		t.Errorf("expected the dispatched subagent's prompt to render in the dialog, got:\n%s", view)
	}
	if !strings.Contains(view, "found 0 leaks across 12 packages") {
		t.Errorf("expected the dispatched subagent's output to render in the dialog, got:\n%s", view)
	}
}
