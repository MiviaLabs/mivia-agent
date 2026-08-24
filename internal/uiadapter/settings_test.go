package uiadapter_test

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
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
	store := uiadapter.NewSettingsStore(res, state)
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
