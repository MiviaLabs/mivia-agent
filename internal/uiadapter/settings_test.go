package uiadapter_test

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	settingsscreen "github.com/MiviaLabs/mivia-agent/internal/ui/screen/settings"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func drainOK(t *testing.T, h ports.SaveHandle) []ports.SaveState {
	t.Helper()
	var states []ports.SaveState
	for ev := range h.Events() {
		states = append(states, ev.State)
		if ev.State == ports.SaveFailed {
			t.Fatalf("save failed: %s", ev.Message)
		}
	}
	return states
}

func drainWithFailure(h ports.SaveHandle) []ports.SaveState {
	var states []ports.SaveState
	for ev := range h.Events() {
		states = append(states, ev.State)
	}
	return states
}

func setupTestSettings(t *testing.T) ports.Settings {
	t.Helper()
	res := &config.Resolved{
		ProviderName: "zai",
		Model:        "glm-5.2",
	}
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{
		Name:         "reviewer",
		Description:  "code reviewer",
		SystemPrompt: "you are a reviewer",
	})
	state := &cliagents.AgentSessionState{
		Registry: reg,
		ToolBase: tools.NewRegistry(),
	}
	store := uiadapter.NewSettingsStore(nil, res, state)
	return store.Settings()
}

func TestGeneralSettings(t *testing.T) {
	settings := setupTestSettings(t)
	gen := settings.General.General()
	if gen.Theme == "" {
		t.Fatal("expected default theme")
	}

	handle, err := settings.General.Apply(context.Background(), ports.ScopeUser, ports.SetTheme{Name: "custom"})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, handle)
	if settings.General.General().Theme != "custom" {
		t.Errorf("got %q, want 'custom'", settings.General.General().Theme)
	}

	h2, err := settings.General.Apply(context.Background(), ports.ScopeUser, ports.SetMouse{On: false})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h2)
	if settings.General.General().Mouse {
		t.Error("expected Mouse=false")
	}

	h3, err := settings.General.Apply(context.Background(), ports.ScopeUser, ports.SetScrollLines{N: 5})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h3)
	if settings.General.General().ScrollLines != 5 {
		t.Errorf("got ScrollLines=%d, want 5", settings.General.General().ScrollLines)
	}

	hIter, err := settings.General.Apply(context.Background(), ports.ScopeUser, ports.SetShowIterationNotices{On: true})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, hIter)
	if !settings.General.General().ShowIterationNotices {
		t.Error("expected ShowIterationNotices=true")
	}

	hCache, err := settings.General.Apply(context.Background(), ports.ScopeUser, ports.SetShowPromptCacheNotices{On: true})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, hCache)
	if !settings.General.General().ShowPromptCacheNotices {
		t.Error("expected ShowPromptCacheNotices=true")
	}

	h4, err := settings.General.Apply(context.Background(), ports.ScopeUser, ports.SetScrollLines{N: -1})
	if err != nil {
		t.Fatal(err)
	}
	states := drainWithFailure(h4)
	if states[len(states)-1] != ports.SaveFailed {
		t.Error("expected SaveFailed on invalid scroll lines")
	}
}

func TestProviderSettings(t *testing.T) {
	settings := setupTestSettings(t)

	// Upsert provider
	h, err := settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.UpsertProvider{
		Provider: ports.ProviderView{
			Name: "ollama",
			Models: []ports.ModelView{
				{Name: "llama3.2", ContextWindowTokens: 128000},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Upsert model
	h, err = settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.UpsertModel{
		Provider: "ollama",
		Model:    ports.ModelView{Name: "llama3.3", ContextWindowTokens: 128000},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Activate model
	h, err = settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.ActivateModel{
		Provider: "ollama",
		Model:    "llama3.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Remove model
	h, err = settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.RemoveModel{
		Provider: "ollama",
		Model:    "llama3.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Remove provider
	h, err = settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.RemoveProvider{
		Name: "ollama",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)
}

func TestMCPSettings(t *testing.T) {
	settings := setupTestSettings(t)

	// Upsert MCP server
	h, err := settings.MCP.Apply(context.Background(), ports.ScopeUser, ports.UpsertMCPServer{
		Server: ports.MCPServerView{
			ID:        "fs",
			Transport: "stdio",
			Command:   "npx",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Toggle enabled
	h, err = settings.MCP.Apply(context.Background(), ports.ScopeUser, ports.SetMCPServerEnabled{
		ID: "fs",
		On: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Remove MCP server
	h, err = settings.MCP.Apply(context.Background(), ports.ScopeUser, ports.RemoveMCPServer{
		ID: "fs",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)
}

func TestAgentSettings(t *testing.T) {
	settings := setupTestSettings(t)

	// Upsert agent
	h, err := settings.Agents.Apply(context.Background(), ports.ScopeUser, ports.UpsertAgent{
		Agent: ports.AgentView{
			Name:        "coder",
			Description: "software developer",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Remove default agent fails
	h, err = settings.Agents.Apply(context.Background(), ports.ScopeUser, ports.RemoveAgent{
		Name: ports.DefaultAgentName,
	})
	if err != nil {
		t.Fatal(err)
	}
	states := drainWithFailure(h)
	if states[len(states)-1] != ports.SaveFailed {
		t.Error("expected SaveFailed when removing default agent")
	}

	// Remove custom agent
	h, err = settings.Agents.Apply(context.Background(), ports.ScopeUser, ports.RemoveAgent{
		Name: "coder",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)
}

func TestAutomationSettings(t *testing.T) {
	settings := setupTestSettings(t)

	// Upsert automation
	h, err := settings.Automations.Apply(context.Background(), ports.ScopeUser, ports.UpsertAutomation{
		Automation: ports.Automation{
			ID:   "auto-1",
			Name: "Daily Review",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Watch automation
	watch, err := settings.Automations.Watch(context.Background(), "auto-1")
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Cancel()

	// Trigger automation
	h, err = settings.Automations.Apply(context.Background(), ports.ScopeUser, ports.TriggerAutomation{
		ID: "auto-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Check runs
	runs := settings.Automations.Runs("auto-1", 5)
	if len(runs) == 0 {
		t.Error("expected runs recorded for auto-1")
	}

	// Toggle automation
	h, err = settings.Automations.Apply(context.Background(), ports.ScopeUser, ports.SetAutomationEnabled{
		ID: "auto-1",
		On: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Remove automation
	h, err = settings.Automations.Apply(context.Background(), ports.ScopeUser, ports.RemoveAutomation{
		ID: "auto-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)
}

func TestProviderSettings_ActivateModelWithSession(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "zai",
		Model:        "glm-5.2",
	}
	sess := chat.NewSession(res, nil)
	sess.SelectModel("glm-5.2")

	reg := agents.NewRegistry()
	state := &cliagents.AgentSessionState{
		Registry: reg,
		ToolBase: tools.NewRegistry(),
	}

	store := uiadapter.NewSettingsStore(sess, res, state)
	settings := store.Settings()

	// Initial active model in session and settings
	providers := settings.Providers.Providers()
	var zai ports.ProviderView
	for _, p := range providers {
		if p.Name == "zai" {
			zai = p
			break
		}
	}
	if !zai.Active || zai.ActiveModel != "glm-5.2" {
		t.Fatalf("expected zai active with glm-5.2, got active=%v, activeModel=%q", zai.Active, zai.ActiveModel)
	}

	// Add new model under zai and activate it
	h, err := settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.UpsertModel{
		Provider: "zai",
		Model:    ports.ModelView{Name: "glm-4.7", ContextWindowTokens: 128000},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	h, err = settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.ActivateModel{
		Provider: "zai",
		Model:    "glm-4.7",
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Verify session model is switched
	if sess.CurrentModel() != "glm-4.7" {
		t.Errorf("expected session model 'glm-4.7', got %q", sess.CurrentModel())
	}
	if res.Model != "glm-4.7" {
		t.Errorf("expected res.Model 'glm-4.7', got %q", res.Model)
	}

	// Verify settings view reflects active model
	providers = settings.Providers.Providers()
	for _, p := range providers {
		if p.Name == "zai" {
			if !p.Active || p.ActiveModel != "glm-4.7" {
				t.Errorf("expected zai active with glm-4.7, got active=%v, activeModel=%q", p.Active, p.ActiveModel)
			}
		}
	}
}

func setupIntegrationEnvironment(t *testing.T, initialModel string) (*chat.Session, *config.Resolved, *cliagents.AgentSessionState) {
	t.Helper()
	res := &config.Resolved{
		ProviderName: "ollama",
		Model:        initialModel,
		Models:       []string{"llama3.2", "llama3.3", "qwen2.5"},
		ModelProfiles: []config.ModelSpec{
			{Name: "llama3.2", ContextWindowTokens: 128000},
			{Name: "llama3.3", ContextWindowTokens: 128000},
			{Name: "qwen2.5", ContextWindowTokens: 128000},
		},
		ProviderRuntimes: map[string]config.ProviderRuntime{
			"ollama": {
				ProviderName: "ollama",
				BaseURL:      "http://127.0.0.1:11434",
				Models: []config.ModelSpec{
					{Name: "llama3.2", ContextWindowTokens: 128000},
					{Name: "llama3.3", ContextWindowTokens: 128000},
					{Name: "qwen2.5", ContextWindowTokens: 128000},
				},
			},
		},
	}
	res.SetModelCatalogForTest([]config.ProviderModelGroup{
		{
			Provider:   "ollama",
			Selectable: true,
			Active:     true,
			Models: []config.ModelSpec{
				{Name: "llama3.2", ContextWindowTokens: 128000},
				{Name: "llama3.3", ContextWindowTokens: 128000},
				{Name: "qwen2.5", ContextWindowTokens: 128000},
			},
		},
	})

	completer := &scriptedCompleter{
		turns: []provider.Response{
			{Content: "Hello from test model", FinishReason: "stop"},
		},
	}
	sess := chat.NewSession(res, completer)
	sess.UseTools = true
	sess.Tools = tools.NewRegistry()
	sess.Tools.Register(noopTool{})
	sess.SelectModel(initialModel)

	reg := agents.NewRegistry()
	state := &cliagents.AgentSessionState{
		Registry: reg,
		ToolBase: tools.NewRegistry(),
	}
	return sess, res, state
}

func TestIntegration_SettingsModelActivationSwitchesSessionAndNextTurn(t *testing.T) {
	sess, res, state := setupIntegrationEnvironment(t, "llama3.2")
	conv := uiadapter.NewConversation(sess)
	store := uiadapter.NewSettingsStore(sess, res, state)
	settings := store.Settings()

	if got := conv.Model().Name; got != "llama3.2" {
		t.Fatalf("expected initial conv model 'llama3.2', got %q", got)
	}
	if got := sess.CurrentModel(); got != "llama3.2" {
		t.Fatalf("expected initial session model 'llama3.2', got %q", got)
	}

	handle, err := settings.Providers.Apply(context.Background(), ports.ScopeUser, ports.ActivateModel{
		Provider: "ollama",
		Model:    "llama3.3",
	})
	if err != nil {
		t.Fatalf("Apply ActivateModel error: %v", err)
	}
	drainOK(t, handle)

	if got := sess.CurrentModel(); got != "llama3.3" {
		t.Errorf("expected session model switched to 'llama3.3', got %q", got)
	}
	if got := conv.Model().Name; got != "llama3.3" {
		t.Errorf("expected conv model switched to 'llama3.3', got %q", got)
	}
	if res.Model != "llama3.3" {
		t.Errorf("expected res.Model 'llama3.3', got %q", res.Model)
	}

	turnHandle, err := conv.Send(context.Background(), intent.Send{Text: "Test query"})
	if err != nil {
		t.Fatalf("conv.Send error: %v", err)
	}
	events := drainUntilClose(t, turnHandle.Events(), 3*time.Second)
	if len(events) == 0 {
		t.Fatal("expected events from turn execution")
	}

	if got := sess.CurrentModel(); got != "llama3.3" {
		t.Errorf("expected session model to remain 'llama3.3' after turn, got %q", got)
	}
	if got := conv.Model().Name; got != "llama3.3" {
		t.Errorf("expected conv model to remain 'llama3.3' after turn, got %q", got)
	}
}

func TestIntegration_CommandRunnerSelectModelSwitchesSessionAndConversation(t *testing.T) {
	sess, res, state := setupIntegrationEnvironment(t, "llama3.2")
	conv := uiadapter.NewConversation(sess)
	runner := uiadapter.NewCommandRunner(sess, res, state)

	outcome := runner.SelectModel(context.Background(), "llama3.3")
	if outcome.Err != "" {
		t.Fatalf("SelectModel error: %s", outcome.Err)
	}

	if got := sess.CurrentModel(); got != "llama3.3" {
		t.Errorf("expected session model 'llama3.3', got %q", got)
	}
	if got := conv.Model().Name; got != "llama3.3" {
		t.Errorf("expected conv model 'llama3.3', got %q", got)
	}
}

func TestIntegration_SettingsModalPopUpdatesConversationTopbarView(t *testing.T) {
	sess, res, state := setupIntegrationEnvironment(t, "llama3.2")
	conv := uiadapter.NewConversation(sess)
	store := uiadapter.NewSettingsStore(sess, res, state)
	approver := uiadapter.NewApprover(sess)

	th := loadTestTheme(t)
	themes := []theme.Theme{th}

	convScreen := conversation.New(th, theme.TierASCII, themes, conv, approver, 80, nil)
	convScreen.SetSettings(store.Settings())

	root := app.New(convScreen, th, theme.TierASCII, themes)

	view := root.View().Content
	if !strings.Contains(view, "llama3.2") {
		t.Fatalf("expected initial root view to contain 'llama3.2', got:\n%s", view)
	}

	top := topbar.New(th, theme.TierASCII, conv.Model(), conv.ContextUsage(), 80)
	settingsSc := settingsscreen.New(th, theme.TierASCII, top, store.Settings(), 5)
	next, _ := root.Update(app.PushScreenMsg{Screen: settingsSc})
	root = next.(app.Model)

	handle, err := store.Settings().Providers.Apply(context.Background(), ports.ScopeUser, ports.ActivateModel{
		Provider: "ollama",
		Model:    "llama3.3",
	})
	if err != nil {
		t.Fatalf("Apply ActivateModel error: %v", err)
	}
	drainOK(t, handle)

	next, _ = root.Update(app.PopScreenMsg{})
	root = next.(app.Model)

	viewAfterPop := root.View().Content
	if !strings.Contains(viewAfterPop, "llama3.3") {
		t.Errorf("expected root view after pop to contain 'llama3.3', got:\n%s", viewAfterPop)
	}
}

func TestIntegration_SlashModelOpensPickerAndSwitchesModel(t *testing.T) {
	sess, res, state := setupIntegrationEnvironment(t, "llama3.2")
	conv := uiadapter.NewConversation(sess)
	runner := uiadapter.NewCommandRunner(sess, res, state)
	approver := uiadapter.NewApprover(sess)

	th := loadTestTheme(t)
	themes := []theme.Theme{th}

	convScreen := conversation.New(th, theme.TierASCII, themes, conv, approver, 80, nil)
	convScreen.SetCommandRunner(runner)

	root := app.New(convScreen, th, theme.TierASCII, themes)
	next, _ := root.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	root = next.(app.Model)

	// Type "/model" and submit to open the picker dialog
	for _, ch := range "/model" {
		next, _ = root.Update(tea.KeyPressMsg{Text: string(ch), Code: ch})
		root = next.(app.Model)
	}
	next, _ = root.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	root = next.(app.Model)

	// Verify the dialog is visible in the view
	view := root.View().Content
	if !strings.Contains(view, "select a model") {
		t.Fatalf("expected view to contain 'select a model' dialog, got:\n%s", view)
	}

	// Press Enter to accept the first model
	next, _ = root.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	root = next.(app.Model)

	// Verify the picker dialog is closed and a notice was appended
	viewAfterSelect := root.View().Content
	if strings.Contains(viewAfterSelect, "select a model") {
		t.Errorf("expected 'select a model' dialog to be closed after selection, got:\n%s", viewAfterSelect)
	}
	if !strings.Contains(viewAfterSelect, "Model set to") {
		t.Errorf("expected view to contain 'Model set to' confirmation notice, got:\n%s", viewAfterSelect)
	}
}

func loadTestTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func TestSettingsStore_SetActiveSession_SwitchesModelOnActiveSession(t *testing.T) {
	sess1, res, state := setupIntegrationEnvironment(t, "llama3.2")
	sess2, _, _ := setupIntegrationEnvironment(t, "llama3.2")
	sess2.SessionID = "sess-2"

	store := uiadapter.NewSettingsStore(sess1, res, state)
	// Update active session to sess2
	store.SetActiveSession(sess2)

	handle, err := store.Settings().Providers.Apply(context.Background(), ports.ScopeUser, ports.ActivateModel{
		Provider: "ollama",
		Model:    "llama3.3",
	})
	if err != nil {
		t.Fatalf("Apply ActivateModel error: %v", err)
	}
	drainOK(t, handle)

	// sess2 must be updated to llama3.3
	if got := sess2.CurrentModel(); got != "llama3.3" {
		t.Errorf("sess2 model = %q, want 'llama3.3'", got)
	}
	// sess1 must remain on llama3.2
	if got := sess1.CurrentModel(); got != "llama3.2" {
		t.Errorf("sess1 model = %q, want 'llama3.2'", got)
	}
}
