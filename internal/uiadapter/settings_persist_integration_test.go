package uiadapter_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func TestSettingsStore_ApplyGeneral_LiveSyncAndPersist(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")

	res := &config.Resolved{
		ConfigPath:   cfgPath,
		ProviderName: "ollama",
		Model:        "llama3.3",
	}
	state := &cliagents.AgentSessionState{
		Registry: agents.NewRegistry(),
	}
	store := uiadapter.NewSettingsStore(nil, res, state)

	conv := uiadapter.NewConversation(nil)
	store.SetConversation(conv)

	// Set scroll lines
	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetScrollLines{N: 7})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	if got := conv.ScrollLines(); got != 7 {
		t.Errorf("conv scroll lines = %d, want 7", got)
	}
	if got := store.Settings().General.General().ScrollLines; got != 7 {
		t.Errorf("general view scroll lines = %d, want 7", got)
	}

	// Set show reasoning
	h, err = store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetShowReasoning{On: false})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	if got := conv.ShowReasoning(); got != false {
		t.Errorf("conv show reasoning = %v, want false", got)
	}

	// Set approval default
	h, err = store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetApprovalDefault{Mode: "always"})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	if got := store.Settings().General.General().ApprovalDefault; got != "always" {
		t.Errorf("approval default = %q, want always", got)
	}
}

func TestSettingsStore_ApplyMCP_Persist(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")

	res := &config.Resolved{
		ConfigPath:   cfgPath,
		ProviderName: "ollama",
		Model:        "llama3.3",
	}
	state := &cliagents.AgentSessionState{
		Registry: agents.NewRegistry(),
	}
	store := uiadapter.NewSettingsStore(nil, res, state)

	// Upsert MCP server
	h, err := store.Settings().MCP.Apply(context.Background(), ports.ScopeProject, ports.UpsertMCPServer{
		Server: ports.MCPServerView{
			ID:        "fetch-srv",
			Transport: "stdio",
			Command:   "uvx",
			Args:      []string{"mcp-server-fetch"},
			Enabled:   true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	servers := store.Settings().MCP.MCPServers()
	if len(servers) != 1 || servers[0].ID != "fetch-srv" {
		t.Fatalf("unexpected mcp servers: %+v", servers)
	}

	// Remove MCP server
	h, err = store.Settings().MCP.Apply(context.Background(), ports.ScopeProject, ports.RemoveMCPServer{ID: "fetch-srv"})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	servers = store.Settings().MCP.MCPServers()
	if len(servers) != 0 {
		t.Fatalf("expected 0 mcp servers after remove, got: %+v", servers)
	}
}

func TestSettingsStore_ApplyAgent_Persist(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")

	res := &config.Resolved{
		ConfigPath:   cfgPath,
		ProviderName: "ollama",
		Model:        "llama3.3",
	}
	state := &cliagents.AgentSessionState{
		Registry: agents.NewRegistry(),
	}
	store := uiadapter.NewSettingsStore(nil, res, state)

	// Upsert Agent
	h, err := store.Settings().Agents.Apply(context.Background(), ports.ScopeProject, ports.UpsertAgent{
		Agent: ports.AgentView{
			Name:        "custom-planner",
			Description: "Custom task planner",
			Provider:    "deepseek",
			Model:       "deepseek-v4-flash",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	agentsList := store.Settings().Agents.Agents()
	found := false
	for _, a := range agentsList {
		if a.Name == "custom-planner" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("custom-planner agent not found in store")
	}

	// Default agent cannot be removed
	h, err = store.Settings().Agents.Apply(context.Background(), ports.ScopeProject, ports.RemoveAgent{Name: ports.DefaultAgentName})
	if err != nil {
		t.Fatal(err)
	}
	states := drainWithFailure(h)
	if len(states) == 0 || states[len(states)-1] != ports.SaveFailed {
		t.Errorf("expected SaveFailed when removing default agent, got: %+v", states)
	}

	// Remove custom-planner agent
	h, err = store.Settings().Agents.Apply(context.Background(), ports.ScopeProject, ports.RemoveAgent{Name: "custom-planner"})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)
}
