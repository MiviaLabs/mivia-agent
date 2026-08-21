package legacytui

import (
	"context"
	"encoding/json"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"io"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestAgentListRowsMarksCurrent(t *testing.T) {
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{Name: "alpha", Description: "A", EffectiveTools: []string{"read_file"}})
	_ = reg.Publish(agents.ResolvedAgent{Name: "beta", Description: "B", EffectiveTools: []string{"read_file"}})
	rows := agentListRows(reg, "beta")
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
	// Names are sorted.
	if rows[0].Name != "alpha" || rows[1].Name != "beta" {
		t.Fatalf("order=%v %v", rows[0].Name, rows[1].Name)
	}
	if rows[0].Current || !rows[1].Current {
		t.Fatalf("current markers wrong: %+v", rows)
	}
	if rows[0].Description != "A" {
		t.Fatalf("desc=%q", rows[0].Description)
	}
}

func TestApplySessionAgentUnknownListsAvailable(t *testing.T) {
	state := fixtureAgentState(t, map[string]string{
		"mivia": "prompt-mivia",
	})
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	err := cli.ApplySessionAgent(sess, nil, state, "nope", false)
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "mivia") {
		t.Fatalf("available list missing: %v", err)
	}
}

func TestApplySessionAgentBusyRefused(t *testing.T) {
	state := fixtureAgentState(t, map[string]string{"mivia": "p"})
	sess := chat.NewSession(&config.Resolved{Model: "m", ProviderName: "p"}, stubAgentCompleter{})
	err := cli.ApplySessionAgent(sess, nil, state, "mivia", true)
	if err == nil || !strings.Contains(err.Error(), "finish current work") {
		t.Fatalf("err=%v", err)
	}
}

func TestApplySessionAgentSwitchesPromptAndSteps(t *testing.T) {
	state := fixtureAgentStateWithTools(t, map[string]agentFixture{
		"reader": {prompt: "READER-PROMPT", tools: []string{"read_file"}, maxTurns: intPtr(3)},
		"writer": {prompt: "WRITER-PROMPT", tools: []string{"read_file", "write_file"}, maxTurns: intPtr(0)},
	})
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	full := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws})
	// Seed tool base as if attach registered session tools on full registry.
	state.ToolBase = full.Clone()

	res := &config.Resolved{Model: "m", ProviderName: "p", Subagents: config.DefaultSubagentConfig}
	sess := chat.NewSession(res, stubAgentCompleter{})
	sess.Tools = full.Clone()
	sess.UseTools = true

	if err := cli.ApplySessionAgent(sess, res, state, "reader", false); err != nil {
		t.Fatal(err)
	}
	if sess.SystemPrompt != "READER-PROMPT" {
		t.Fatalf("prompt=%q", sess.SystemPrompt)
	}
	if sess.MaxStepsValue() != 3 {
		t.Fatalf("steps=%d", sess.MaxStepsValue())
	}
	if _, ok := sess.Tools.Get("write_file"); ok {
		t.Fatal("reader must not keep write_file")
	}
	if _, ok := sess.Tools.Get("read_file"); !ok {
		t.Fatal("reader must keep read_file")
	}

	if err := cli.ApplySessionAgent(sess, res, state, "writer", false); err != nil {
		t.Fatal(err)
	}
	if sess.SystemPrompt != "WRITER-PROMPT" {
		t.Fatalf("prompt=%q", sess.SystemPrompt)
	}
	if sess.MaxStepsValue() != 0 {
		t.Fatalf("unlimited steps want 0 got %d", sess.MaxStepsValue())
	}
	if _, ok := sess.Tools.Get("write_file"); !ok {
		t.Fatal("writer must regain write_file after switch")
	}
	// Execution denial: write should work for writer.
	if _, err := sess.Tools.Execute(context.Background(), "write_file", json.RawMessage(`{"path":"x.txt","content":"ok"}`)); err != nil {
		t.Fatalf("write after switch: %v", err)
	}
}

func TestSlashCatalogIncludesAgent(t *testing.T) {
	if _, ok := cli.FindSlashCommand("/agent", cli.SlashSurfaceTUI, nil); !ok {
		t.Fatal("/agent missing from TUI catalog")
	}
}

func TestAgentDialogOpenAndSelect(t *testing.T) {
	state := fixtureAgentState(t, map[string]string{
		"mivia":       "M",
		"go-engineer": "G",
	})
	// Minimal tools base so apply does not require dispatcher rebuild with nil tools.
	// Tools nil path only updates prompt.
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	m := newTUIModel(sess, res, false)
	m.agentState = state
	m.width, m.height = 80, 24

	if !m.handleSlash("/agent") {
		t.Fatal("bare /agent not handled")
	}
	if m.agentDlg == nil {
		t.Fatal("dialog not opened")
	}
	// Current should mark mivia if selected is mivia - set selection.
	state.Selected = &agents.ResolvedAgent{Name: "mivia", SystemPrompt: "M"}
	m.openAgentDialog()
	found := false
	for _, row := range m.agentDlg.rows {
		if row.Name == "mivia" && row.Current {
			found = true
		}
	}
	if !found {
		t.Fatalf("current marker missing: %+v", m.agentDlg.rows)
	}

	// Direct switch.
	if !m.handleSlash("/agent go-engineer") {
		t.Fatal("direct /agent not handled")
	}
	if m.agentState.Selected == nil || m.agentState.Selected.Name != "go-engineer" {
		t.Fatalf("selected=%v", m.agentState.Selected)
	}
	if sess.SystemPrompt != "G" {
		t.Fatalf("prompt=%q", sess.SystemPrompt)
	}
}

func TestClassicSlashAgentDirect(t *testing.T) {
	state := fixtureAgentState(t, map[string]string{"alpha": "A", "beta": "B"})
	*cli.ClassicAgentStatePtr = state
	defer func() { *cli.ClassicAgentStatePtr = nil }()
	res := &config.Resolved{Model: "m", ProviderName: "p"}
	sess := chat.NewSession(res, stubAgentCompleter{})
	term, err := cli.NewTerminal()
	if err != nil {
		// Non-TTY environments still exercise apply via cli.HandleSlashAgent.
		handled, _, herr := cli.HandleSlashAgent([]string{"/agent", "beta"}, sess, res, nil, state)
		if !handled || herr != nil {
			t.Fatalf("handled=%v err=%v", handled, herr)
		}
	} else {
		defer term.Close()
		handled, _, herr := cli.HandleSlash("/agent beta", sess, res, false, term)
		if !handled || herr != nil {
			t.Fatalf("handled=%v err=%v", handled, herr)
		}
	}
	if state.Selected == nil || state.Selected.Name != "beta" {
		t.Fatalf("selected=%v", state.Selected)
	}
	if sess.SystemPrompt != "B" {
		t.Fatalf("prompt=%q", sess.SystemPrompt)
	}
}

// --- fixtures ---

type agentFixture struct {
	prompt   string
	tools    []string
	maxTurns *int
}

func intPtr(n int) *int { return &n }

func fixtureAgentState(t *testing.T, prompts map[string]string) *cli.AgentSessionState {
	t.Helper()
	fx := make(map[string]agentFixture, len(prompts))
	for name, p := range prompts {
		fx[name] = agentFixture{prompt: p, tools: []string{"read_file"}}
	}
	return fixtureAgentStateWithTools(t, fx)
}

func fixtureAgentStateWithTools(t *testing.T, fx map[string]agentFixture) *cli.AgentSessionState {
	t.Helper()
	reg := agents.NewRegistry()
	for name, f := range fx {
		a := agents.ResolvedAgent{
			Name:           name,
			Description:    name + " desc",
			SystemPrompt:   f.prompt,
			EffectiveTools: append([]string(nil), f.tools...),
			MaxTurns:       f.maxTurns,
		}
		if err := reg.Publish(a); err != nil {
			t.Fatal(err)
		}
	}
	return &cli.AgentSessionState{
		Registry:           reg,
		AllowProjectSkills: true,
		WorkspaceRoot:      t.TempDir(),
		Global:             config.AgentsGlobal{FailOnEmptyToolset: true},
	}
}

type stubAgentCompleter struct{}

func (stubAgentCompleter) Name() string { return "stub" }
func (stubAgentCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "ok", nil
}
func (stubAgentCompleter) ChatStream(_ context.Context, _ provider.Request, w io.Writer) (string, error) {
	_, _ = io.WriteString(w, "ok")
	return "ok", nil
}
func (stubAgentCompleter) ChatTurn(context.Context, provider.Request) (*provider.Response, error) {
	return &provider.Response{Content: "ok"}, nil
}
