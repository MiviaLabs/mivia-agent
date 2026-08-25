package uiadapter_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"

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

func TestSettingsStore_ApplyProject_Persist(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")

	res := &config.Resolved{
		ConfigPath:   cfgPath,
		ProviderName: "ollama",
		Model:        "llama3.3",
	}
	state := &cliagents.AgentSessionState{
		Registry:      agents.NewRegistry(),
		WorkspaceRoot: tmpDir,
	}
	store := uiadapter.NewSettingsStore(nil, res, state)

	applyProjectTestEdits(t, store)

	// 1. Check in-memory store
	p := store.Settings().Projects.Project()
	assertInMemoryProjectView(t, p)

	// 2. Read back from disk to verify TOML persistence
	writtenPath := p.ConfigPath
	if writtenPath == "" {
		writtenPath = cfgPath
	}
	assertPersistedProjectTOML(t, writtenPath)
}

func applyProjectTestEdits(t *testing.T, store *uiadapter.SettingsStore) {
	t.Helper()
	edits := []ports.ProjectEdit{
		ports.SetProjectEnvFile{Path: ".env.production"},
		ports.SetProjectBranchPrefix{Prefix: "feat/persist-"},
		ports.SetProjectSystemPrompt{Prompt: "Persisted project instructions"},
		ports.SetProjectTemperature{Value: "0.4"},
		ports.SetProjectMaxTokens{Value: "16384"},
		ports.SetProjectMaxPromptTokens{Value: "32768"},
		ports.SetProjectMaxSteps{Value: "50"},
		ports.SetProjectRunTimeout{Seconds: 1800},
		ports.SetProjectStoreBackend{Backend: "sqlite"},
		ports.SetProjectStorePath{Path: ".mivia/custom_store.db"},
		ports.SetProjectSandbox{On: false},
		ports.SetProjectRedactToolArgs{On: true},
	}
	for _, edit := range edits {
		h, err := store.Settings().Projects.Apply(context.Background(), ports.ScopeProject, edit)
		if err != nil {
			t.Fatalf("failed to apply edit %T: %v", edit, err)
		}
		drainOK(t, h)
	}
}

func assertInMemoryProjectView(t *testing.T, p ports.ProjectView) {
	t.Helper()
	if p.EnvFile != ".env.production" || p.BranchPrefix != "feat/persist-" {
		t.Errorf("env/branch mismatch: %+v", p)
	}
	if p.SystemPrompt != "Persisted project instructions" || p.Temperature != "0.4" {
		t.Errorf("prompt/temp mismatch: %+v", p)
	}
	if p.MaxTokens != "16384" || p.MaxPromptTokens != "32768" || p.MaxSteps != "50" {
		t.Errorf("token/step limits mismatch: %+v", p)
	}
	if p.RunTimeoutSec != 1800 || p.StoreBackend != "sqlite" || p.StorePath != ".mivia/custom_store.db" {
		t.Errorf("store/timeout mismatch: %+v", p)
	}
	if p.Sandbox || !p.RedactToolArgs {
		t.Errorf("flags mismatch: %+v", p)
	}
}

func assertPersistedProjectTOML(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read persisted TOML from %q: %v", path, err)
	}
	var raw struct {
		EnvFile   string `toml:"env_file"`
		Worktrees struct {
			BranchPrefix string `toml:"branch_prefix"`
		} `toml:"worktrees"`
		Chat struct {
			SystemPrompt    string  `toml:"system_prompt"`
			Temperature     float64 `toml:"temperature"`
			MaxTokens       int     `toml:"max_tokens"`
			MaxPromptTokens int     `toml:"max_prompt_tokens"`
			MaxSteps        int     `toml:"max_steps"`
		} `toml:"chat"`
		Tools struct {
			RunTimeoutSec int `toml:"run_timeout_seconds"`
		} `toml:"tools"`
		Subagents struct {
			StoreBackend string `toml:"store_backend"`
			StorePath    string `toml:"store_path"`
		} `toml:"subagents"`
		Harness struct {
			Sandbox bool `toml:"sandbox"`
		} `toml:"harness"`
		Privacy struct {
			RedactToolArgs bool `toml:"redact_tool_args"`
		} `toml:"privacy"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal persisted TOML: %v\ncontent: %s", err, string(data))
	}
	if raw.EnvFile != ".env.production" || raw.Worktrees.BranchPrefix != "feat/persist-" {
		t.Errorf("TOML env/branch mismatch: %+v", raw)
	}
	if raw.Chat.SystemPrompt != "Persisted project instructions" || raw.Chat.Temperature != 0.4 {
		t.Errorf("TOML prompt/temp mismatch: %+v", raw.Chat)
	}
	if raw.Chat.MaxTokens != 16384 || raw.Chat.MaxPromptTokens != 32768 || raw.Chat.MaxSteps != 50 {
		t.Errorf("TOML limits mismatch: %+v", raw.Chat)
	}
	if raw.Tools.RunTimeoutSec != 1800 || raw.Subagents.StoreBackend != "sqlite" {
		t.Errorf("TOML tools/subagents mismatch: %+v", raw)
	}
	if raw.Subagents.StorePath != ".mivia/custom_store.db" || raw.Harness.Sandbox || !raw.Privacy.RedactToolArgs {
		t.Errorf("TOML flags mismatch: %+v", raw)
	}
}
