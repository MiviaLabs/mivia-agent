package uiadapter_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextmgr"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

func TestCommandRunner_RunBasicCommands(t *testing.T) {
	runner := uiadapter.NewCommandRunner(nil, nil, nil)

	// Theme
	if out := runner.Run(context.Background(), "theme", ""); !out.OpenTheme {
		t.Errorf("expected OpenTheme=true, got %+v", out)
	}

	// Help
	if out := runner.Run(context.Background(), "help", ""); !out.OpenHelp {
		t.Errorf("expected OpenHelp=true, got %+v", out)
	}

	// Settings
	if out := runner.Run(context.Background(), "settings", "models"); !out.OpenSettings || out.SettingsSection != "models" {
		t.Errorf("expected OpenSettings=true with models section, got %+v", out)
	}

	// Quit
	if out := runner.Run(context.Background(), "quit", ""); !out.Quit {
		t.Errorf("expected Quit=true, got %+v", out)
	}
	if out := runner.Run(context.Background(), "exit", ""); !out.Quit {
		t.Errorf("expected Quit=true on exit, got %+v", out)
	}

	// Unknown
	if out := runner.Run(context.Background(), "foobar", "baz"); out.Err == "" {
		t.Errorf("expected Err on unknown command, got %+v", out)
	}
}

func TestCommandRunner_NilSessionErrors(t *testing.T) {
	runner := uiadapter.NewCommandRunner(nil, nil, nil)

	if out := runner.Run(context.Background(), "clear", ""); out.Err == "" {
		t.Errorf("expected Err on nil session clear, got %+v", out)
	}
	if out := runner.Run(context.Background(), "compact", ""); out.Err == "" {
		t.Errorf("expected Err on nil session compact, got %+v", out)
	}
	if out := runner.Run(context.Background(), "context", ""); out.Err == "" {
		t.Errorf("expected Err on nil session context, got %+v", out)
	}
	if out := runner.Run(context.Background(), "cost", ""); out.Err == "" {
		t.Errorf("expected Err on nil session cost, got %+v", out)
	}
	if out := runner.Run(context.Background(), "model", ""); out.Err == "" {
		t.Errorf("expected Err on nil session model, got %+v", out)
	}
	if out := runner.SelectModel(context.Background(), "m1"); out.Err == "" {
		t.Errorf("expected Err on nil session SelectModel, got %+v", out)
	}
	if out := runner.SelectAgent(context.Background(), "a1"); out.Err == "" {
		t.Errorf("expected Err on nil session SelectAgent, got %+v", out)
	}
	if out := runner.Run(context.Background(), "resume", ""); out.Err == "" {
		t.Errorf("expected Err on nil session resume, got %+v", out)
	}
	if out := runner.SelectSession(context.Background(), "s1"); out.Err == "" {
		t.Errorf("expected Err on nil session SelectSession, got %+v", out)
	}
}

func TestCommandRunner_ClearAndUsage(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// Clear
	if out := runner.Run(context.Background(), "clear", ""); !out.ClearTranscript {
		t.Errorf("expected ClearTranscript=true, got %+v", out)
	}

	// Context
	if out := runner.Run(context.Background(), "context", ""); out.Notice == "" {
		t.Errorf("expected Notice for context, got %+v", out)
	}

	// Cost
	if out := runner.Run(context.Background(), "cost", ""); out.Notice == "" {
		t.Errorf("expected Notice for cost, got %+v", out)
	}
}

func TestCommandRunner_ModelSwitching(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{
		ProviderName: "zai",
		Model:        "glm-5.2",
	}
	sess := chat.NewSession(res, comp)
	sess.SetBindingFactory(func(providerName, model string) (chat.ModelBinding, error) {
		return chat.ModelBinding{
			ProviderName: providerName,
			Model:        model,
			Completer:    comp,
			Profile:      config.ModelSpec{Name: model, ContextWindowTokens: 2000},
		}, nil
	})

	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// Open model picker
	out := runner.Run(context.Background(), "model", "")
	if len(out.ModelChoiceGroups) == 0 {
		t.Fatalf("expected ModelChoiceGroups, got %+v", out)
	}

	// Direct switch via /model glm-5.2
	out = runner.Run(context.Background(), "model", "glm-5.2")
	if out.Err != "" {
		t.Fatalf("Run model glm-5.2 error: %s", out.Err)
	}
	if sess.CurrentModel() != "glm-5.2" {
		t.Errorf("got current model %q, want %q", sess.CurrentModel(), "glm-5.2")
	}

	// SelectModel direct
	out = runner.SelectModel(context.Background(), "glm-5.2")
	if out.Err != "" {
		t.Fatalf("SelectModel error: %s", out.Err)
	}
	if out.Notice == "" {
		t.Errorf("expected confirmation notice on SelectModel, got %+v", out)
	}

	// SelectModel error
	out = runner.SelectModel(context.Background(), "invalid-model")
	if out.Err == "" {
		t.Errorf("expected error on invalid model switch, got %+v", out)
	}
}

func TestCommandRunner_AgentSwitching(t *testing.T) {
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "test", Model: "m1"}
	sess := chat.NewSession(res, comp)
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{
		Name:         "reviewer",
		Description:  "code reviewer",
		SystemPrompt: "you are a reviewer",
	})
	_ = reg.Publish(agents.ResolvedAgent{
		Name:         "coder",
		Description:  "software engineer",
		SystemPrompt: "you are a coder",
	})

	state := &cliagents.AgentSessionState{
		Registry: reg,
		ToolBase: tools.NewRegistry(),
	}

	runner := uiadapter.NewCommandRunner(sess, res, state)

	// List choices
	out := runner.Run(context.Background(), "agents", "")
	if len(out.AgentChoices) < 2 {
		t.Fatalf("expected at least 2 AgentChoices, got %+v", out)
	}

	// Direct switch via /agent reviewer
	out = runner.Run(context.Background(), "agent", "reviewer")
	if out.Err != "" {
		t.Fatalf("Run agent reviewer error: %s", out.Err)
	}

	// SelectAgent
	out = runner.SelectAgent(context.Background(), "reviewer")
	if out.Err != "" {
		t.Fatalf("SelectAgent error: %s", out.Err)
	}
	if out.Notice == "" {
		t.Errorf("expected confirmation notice, got %+v", out)
	}

	// SelectAgent empty name
	if out := runner.SelectAgent(context.Background(), ""); out.Err == "" {
		t.Errorf("expected error on empty agent name, got %+v", out)
	}

	// SelectAgent unknown
	if out := runner.SelectAgent(context.Background(), "unknown"); out.Err == "" {
		t.Errorf("expected error on unknown agent, got %+v", out)
	}
}

func setupSessionStoreFixture(t *testing.T) (*chat.Session, *config.Resolved, *storage.SQLite, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "uiadapter-runner-test-*")
	if err != nil {
		t.Fatal(err)
	}
	comp := &nullCompleter{}
	res := &config.Resolved{ProviderName: "fake", Model: "m1", SystemPrompt: "sys"}
	sess := chat.NewSession(res, comp)
	sess.SessionDir = tmpDir

	store, err := storage.OpenSQLite(tmpDir + "/context.db")
	if err != nil {
		t.Fatal(err)
	}

	principal, err := contextstate.NewPrincipal("workspace", sess.SessionID, "subject")
	if err != nil {
		t.Fatal(err)
	}

	manager := &contextmgr.ContextManager{
		PreparationManager:  contextmgr.StructuralPreparationManager{},
		CheckpointPublisher: contextmgr.PreparationCommitter{Store: store},
		Enabled:             true,
	}
	if err := sess.SetContextManager(manager, principal); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetContextStore(store); err != nil {
		t.Fatal(err)
	}

	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return sess, res, store, cleanup
}

func TestCommandRunner_ResumeSession(t *testing.T) {
	sess, res, _, cleanup := setupSessionStoreFixture(t)
	defer cleanup()
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// Resume on empty store
	if out := runner.Run(context.Background(), "resume", ""); out.Err == "" {
		t.Errorf("expected error or no sessions on empty store, got %+v", out)
	}

	// Save a session so resume has content
	if err := sess.Save("saved-1"); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Run resume list
	out := runner.Run(context.Background(), "resume", "")
	if len(out.SessionChoices) == 0 {
		t.Fatalf("expected SessionChoices, got %+v", out)
	}

	// Direct resume via /resume saved-1
	if out := runner.Run(context.Background(), "resume", "saved-1"); out.Err != "" {
		t.Fatalf("Run resume saved-1 error: %s", out.Err)
	}

	// SelectSession empty
	if out := runner.SelectSession(context.Background(), ""); out.Err == "" {
		t.Errorf("expected error on empty session ID, got %+v", out)
	}

	// SelectSession unknown
	if out := runner.SelectSession(context.Background(), "nonexistent"); out.Err == "" {
		t.Errorf("expected error on nonexistent session, got %+v", out)
	}

	// SelectSession
	out = runner.SelectSession(context.Background(), "saved-1")
	if out.Err != "" {
		t.Fatalf("SelectSession error: %s", out.Err)
	}
	if !out.ClearTranscript {
		t.Error("SelectSession must set ClearTranscript=true")
	}
}

func TestCommandRunner_Compact(t *testing.T) {
	sess, res, _, cleanup := setupSessionStoreFixture(t)
	defer cleanup()
	runner := uiadapter.NewCommandRunner(sess, res, nil)

	// Send a turn and add history for compaction
	if _, err := sess.SendUser(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		sess.Messages = append(sess.Messages,
			provider.Message{Role: provider.RoleUser, Content: strings.Repeat("long question ", 20)},
			provider.Message{Role: provider.RoleAssistant, Content: strings.Repeat("long answer ", 20)},
		)
	}

	// Compact
	out := runner.Run(context.Background(), "compact", "")
	if out.Err != "" {
		t.Fatalf("compact error: %s", out.Err)
	}
	if out.Notice == "" {
		t.Errorf("expected Notice for compact, got %+v", out)
	}
}

func TestDefaultCommands(t *testing.T) {
	cmds := uiadapter.DefaultCommands()
	if len(cmds) == 0 {
		t.Fatal("expected non-empty DefaultCommands")
	}
	var foundModel, foundAgents, foundResume bool
	for _, c := range cmds {
		switch c.Name {
		case "model":
			foundModel = true
		case "agents":
			foundAgents = true
		case "resume":
			foundResume = true
		}
	}
	if !foundModel || !foundAgents || !foundResume {
		t.Errorf("missing core commands in DefaultCommands: %+v", cmds)
	}
}
