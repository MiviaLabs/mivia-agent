package uiadapter_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
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

// TestSettingsStore_ApprovalDefault_ReloadsFromConfig closes the loop this
// bug report was about: a persisted default_mode must survive a fresh
// SettingsStore built over the reloaded config (the CLI-restart /
// session-resume case), not just the in-memory store that wrote it.
// initFromConfig previously hardcoded "once" regardless of what config.Load
// had already resolved into Resolved.Approvals.
func TestSettingsStore_ApprovalDefault_ReloadsFromConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")

	res := &config.Resolved{ConfigPath: cfgPath, ProviderName: "ollama", Model: "llama3.3"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	store := uiadapter.NewSettingsStore(nil, res, state)

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetApprovalDefault{Mode: "always"})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	// Confirm the file itself carries the persisted value...
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	var raw struct {
		Approvals config.ApprovalsConfig `toml:"approvals"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	if raw.Approvals.DefaultMode != "always" {
		t.Fatalf("persisted default_mode = %q, want %q", raw.Approvals.DefaultMode, "always")
	}

	// ...and that a brand-new store built over a Resolved carrying that same
	// reloaded value (what a restart/resume produces via config.Load)
	// reflects it in the settings view instead of resetting to "once".
	reloadedRes := &config.Resolved{ConfigPath: cfgPath, ProviderName: "ollama", Model: "llama3.3", Approvals: raw.Approvals}
	fresh := uiadapter.NewSettingsStore(nil, reloadedRes, state)
	if got := fresh.Settings().General.General().ApprovalDefault; got != "always" {
		t.Errorf("reloaded approval default = %q, want %q (must not reset to \"once\" on restart)", got, "always")
	}
}

// TestSettingsStore_ApprovalDefault_AppliesLiveToSession asserts that
// changing the setting takes effect in the running session immediately,
// not only on the next restart.
func TestSettingsStore_ApprovalDefault_AppliesLiveToSession(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")

	res := &config.Resolved{ConfigPath: cfgPath, ProviderName: "ollama", Model: "llama3.3"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	sess := chat.NewSession(res, nil)
	sess.ApprovalPolicy = config.ApprovalPolicyWriteOnly

	store := uiadapter.NewSettingsStore(nil, res, state)
	store.SetActiveSession(sess)

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetApprovalDefault{Mode: "always"})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	if got := sess.ApprovalPolicyValue(); got != config.ApprovalPolicyAuto {
		t.Errorf("live session ApprovalPolicyValue() = %q, want %q (setting must apply without restart)", got, config.ApprovalPolicyAuto)
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

// TestSettingsStore_ApplySync_IncludeThinking_ToggleOff_RoundTrip drives
// the [sync] include_thinking key through the real SettingsStore end to
// end: the in-memory view updates, the file on disk materialises
// `include_thinking = false`, and a reloaded config (parsed via the
// resolver the real file goes through) reports IncludeThinking == false.
// The test deliberately uses the same hand-parsed resolver path that
// internal/config/sync_enabled_toml_test.go uses for the same absent-vs-
// false contract, rather than the heavier config.Load: the lighter path is
// the one whose behaviour the [sync] key actually depends on, and the
// heavier path drags in provider resolution that has no business in a
// sync-only assertion.
// syncConfigFromMap projects a decoded [sync] TOML map into the SyncConfig
// the production resolver consumes. Used by the integration tests to
// confirm a written file's contents round-trip through the same resolver
// path config.Load eventually invokes, without dragging in the full
// config pipeline (provider resolution, MCP, etc.) that has no business
// in a sync-only assertion.
//
// Lives here (test-only) rather than in the production config package
// because no production caller needs this projection - production code
// always has a SyncConfig in hand by the time it cares.
func syncConfigFromMap(m map[string]any) config.SyncConfig {
	get := func(key string) *bool {
		v, ok := m[key]
		if !ok {
			return nil
		}
		b, ok := v.(bool)
		if !ok {
			return nil
		}
		return &b
	}
	return config.SyncConfig{
		IncludeThinking: get("include_thinking"),
		IncludeToolIO:   get("include_tool_io"),
		StreamAssistant: get("stream_assistant"),
	}
}

func TestSettingsStore_ApplySync_IncludeThinking_ToggleOff_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	res := &config.Resolved{ConfigPath: cfgPath, ProviderName: "ollama", Model: "llama3.3"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	store := uiadapter.NewSettingsStore(nil, res, state)

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetSyncIncludeThinking{On: false})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	if got := store.Settings().General.General().SyncIncludeThinking; got != false {
		t.Errorf("in-memory SyncIncludeThinking = %v, want false", got)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal written config: %v", err)
	}
	syncMap, ok := raw["sync"].(map[string]any)
	if !ok {
		t.Fatalf("missing [sync] table in written file:\n%s", string(data))
	}
	if v, ok := syncMap["include_thinking"]; !ok || v != false {
		t.Errorf("include_thinking = %v (present=%v), want false present:\n%s", v, ok, string(data))
	}

	// Reload via the same resolver the production code uses for [sync].
	resolved := config.ResolveSyncConfig(syncConfigFromMap(syncMap))
	if resolved.IncludeThinking {
		t.Errorf("ResolvedSync.IncludeThinking = true, want false after explicit false write")
	}
}

// TestSettingsStore_ApplySync_IncludeToolIO_ToggleOff_RoundTrip mirrors
// the IncludeThinking test for the include_tool_io switch. Two siblings
// (rather than three tests) is the right number: each test exercises a
// different file-write path through the *bool helper, and the third
// variant (stream_assistant) shares both. The omission is explicit so a
// future regression cannot silently break only one of the three.
func TestSettingsStore_ApplySync_IncludeToolIO_ToggleOff_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	res := &config.Resolved{ConfigPath: cfgPath, ProviderName: "ollama", Model: "llama3.3"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	store := uiadapter.NewSettingsStore(nil, res, state)

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetSyncIncludeToolIO{On: false})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	syncMap := raw["sync"].(map[string]any)
	if v, ok := syncMap["include_tool_io"]; !ok || v != false {
		t.Errorf("include_tool_io = %v (present=%v), want false present", v, ok)
	}
	resolved := config.ResolveSyncConfig(syncConfigFromMap(syncMap))
	if resolved.IncludeToolIO {
		t.Errorf("ResolvedSync.IncludeToolIO = true, want false")
	}
}

// TestSettingsStore_ApplySync_ToggleOffThenOn_WritesTrue closes the
// round-trip loop the planner pinned as a load-bearing rule: an operator
// who toggles include_thinking off then back on must end with the key
// PRESENT and set to true (not absent). The TUI must never silently delete
// the key on a back-to-true toggle, because the documented absent-means-on
// rule means a later cosmetic file rewriter could erase the operator's
// last action without their consent.
func TestSettingsStore_ApplySync_ToggleOffThenOn_WritesTrue(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "mivia.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	res := &config.Resolved{ConfigPath: cfgPath, ProviderName: "ollama", Model: "llama3.3"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	store := uiadapter.NewSettingsStore(nil, res, state)

	// Toggle off.
	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetSyncIncludeThinking{On: false})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)
	// Toggle back on.
	h, err = store.Settings().General.Apply(context.Background(), ports.ScopeProject, ports.SetSyncIncludeThinking{On: true})
	if err != nil {
		t.Fatal(err)
	}
	drainOK(t, h)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	syncMap := raw["sync"].(map[string]any)
	v, present := syncMap["include_thinking"]
	if !present {
		t.Fatalf("include_thinking absent after toggle-off-then-on; expected present=true:\n%s", string(data))
	}
	if v != true {
		t.Errorf("include_thinking = %v after toggle-off-then-on, want true", v)
	}
	resolved := config.ResolveSyncConfig(syncConfigFromMap(syncMap))
	if !resolved.IncludeThinking {
		t.Errorf("ResolvedSync.IncludeThinking = false after toggle-back-on, want true")
	}
}

// TestSettingsStore_ApplySync_RollbackOnPersistFailure asserts the
// rollback contract the locked plan called out: when persist fails, the
// in-memory General view must NOT carry the failed edit. The failing-path
// technique mirrors settings_persist_failure_test.go's unwritableConfigPath
// helper - a config path whose PARENT is a regular file fails at
// MkdirAll for every user, including root, so the assertion holds in CI.
func TestSettingsStore_ApplySync_RollbackOnPersistFailure(t *testing.T) {
	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	unwritablePath := filepath.Join(blocker, "mivia.toml")

	// Build a Resolved through the real resolver with an empty [sync]
	// table, so SyncIncludeThinking/ToolIO/StreamAssistant reflect the
	// file's "absent = ON" default rather than the Go zero value (which
	// would be false and misrepresent the empty file's intent).
	res := &config.Resolved{
		Model:      "test-model",
		ConfigPath: unwritablePath,
		Sync:       config.ResolveSyncConfig(config.SyncConfig{}),
	}
	sess := chat.NewSession(res, nil)
	store := uiadapter.NewSettingsStore(sess, res, nil)

	h, err := store.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetSyncIncludeThinking{On: false})
	if err != nil {
		return // a synchronous refusal is also an honest failure
	}

	var last ports.SaveEvent
	for ev := range h.Events() {
		last = ev
	}
	if last.State != ports.SaveFailed {
		t.Fatalf("terminal state = %v, want SaveFailed: setting did not persist but no failure was surfaced", last.State)
	}
	if got := store.Settings().General.General().SyncIncludeThinking; got != true {
		t.Errorf("post-rollback General.SyncIncludeThinking = %v, want true (prior value must be restored on persist failure)", got)
	}
}
