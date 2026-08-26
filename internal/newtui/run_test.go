package newtui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/charmbracelet/x/ansi"
)

func TestBuildApp(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{}, nil)
	res := &config.Resolved{}
	agentState := &cli.AgentSessionState{}

	appModel, err := buildApp(sess, res, true, agentState, "")
	if err != nil {
		t.Fatalf("buildApp failed: %v", err)
	}
	if appModel == nil {
		t.Fatal("expected non-nil app model")
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

	root, err := buildApp(sess, res, true, agentState, "")
	if err != nil {
		t.Fatalf("buildApp: %v", err)
	}

	m, _ := root.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m, _ = m.Update(ctrl('b')) // open the activity panel, focused on its list
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "perform detailed research on memory leaks") {
		t.Errorf("expected the dispatched subagent's prompt to render in the dialog, got:\n%s", view)
	}
	if !strings.Contains(view, "found 0 leaks across 12 packages") {
		t.Errorf("expected the dispatched subagent's output to render in the dialog, got:\n%s", view)
	}
}
