package clichat

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestRunConfiguredChatAnnouncesGlobalMCPToolToDefaultRootAgent reproduces the
// exact shape of this repo's own .mivia/mivia.toml + .mivia/agents/mivia.toml
// setup: an [mcp] server marked global = true, and a root agent definition
// named config.DefaultAgentName that declares neither `tools` nor
// `mcp_servers` (so it is meant to inherit "every authorized tool" and "every
// global MCP server" respectively). The real chat entrypoint - config, agent
// selection, MCP discovery, dispatcher attach, one-shot turn - runs end to
// end against a stub provider and MCP server, and the request wire payload
// must carry the MCP tool's schema.
func TestRunConfiguredChatAnnouncesGlobalMCPToolToDefaultRootAgent(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	fake := &fakeProviderServer{}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)

	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(ws)

	// No `tools` list: inherit the full authorized catalog, matching this
	// repo's .mivia/agents/mivia.toml ("No `tools` list: the root session
	// uses the full tool catalog."). No `mcp_servers` list either, matching
	// every shipped agent file in this repo: the intent is "pick up every
	// global MCP server", not "opt out unless named".
	writeTestAgent(t, config.WorkspaceAgentsDir(ws), config.DefaultAgentName, `
name = "`+config.DefaultAgentName+`"
description = "root orchestrator"
`)

	res := &config.Resolved{
		ProviderName: "openrouter",
		Model:        "test/model",
		Models:       []string{"test/model"},
		BaseURL:      server.URL,
		APIKey:       "test-key",
		APIKeyEnv:    "TEST_KEY",
		APIKeySet:    true,
		SystemPrompt: "ROOT PROMPT",
		Subagents:    config.DefaultSubagentConfig,
		Tools:        config.ToolsConfig{},
		MCP: config.MCPConfig{
			Enabled:                 true,
			StartupTimeoutSeconds:   10,
			MaxServers:              16,
			MaxToolsPerServer:       64,
			MaxToolSchemaBytes:      64 << 10,
			MaxToolDescriptionBytes: 4 << 10,
			MaxToolResultBytes:      64 << 10,
			Servers: []config.MCPServerConfig{{
				ID: "repo", Transport: "stdio", Command: os.Args[0],
				Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: []string{"MIVIA_CLI_MCP_HELPER"},
				Global: true, TimeoutSeconds: 10,
			}},
		},
	}
	res.Subagents.StoreBackend = "sqlite"
	res.Subagents.StorePath = filepath.Join(t.TempDir(), "context.db")

	// Mirror res.MCP to disk: agents.LoadAndResolveOpts (used to compute
	// EffectiveMCPServers) reads [mcp] from the workspace's own
	// .mivia/mivia.toml independently of the *config.Resolved this test
	// constructs by hand - the two must describe the same server for the
	// production code path this reproduces.
	writeWorkspaceMCPConfig(t, ws, os.Args[0])

	invocation := chatInvocation{prompt: "hello", workspacePath: ws, plainUI: true}
	if err := runConfiguredChat(invocation, res); err != nil {
		t.Fatalf("runConfiguredChat: %v", err)
	}

	requests := fake.advertised()
	if len(requests) == 0 {
		t.Fatal("the stub provider was never called")
	}
	first := requests[0]
	if !slices.Contains(first, "mcp__repo__x6563686f") {
		t.Fatalf("advertised = %v, want the global MCP server's tool (mcp__repo__x6563686f)", first)
	}
}

// TestRunConfiguredChatKeepsGlobalMCPToolInCoreTierEvenWithAConfiguredCoreList
// is the regression test for the root cause behind a configured, successfully
// -connected MCP server (e.g. codegraph) never actually being called: this
// repo's real .mivia/mivia.toml sets [tools] core = [...] (plan tools/05's
// deferred-tool tier) naming only compiled-in, statically-known tool names.
// An MCP tool's name is a runtime hash of the remote tool name
// (internal/mcp.EncodeToolName) that cannot be predicted or written into that
// list ahead of time - before the fix, planToolTiers therefore silently
// deferred EVERY MCP tool the moment ANY core list was configured, exactly
// like grep/glob do in TestRunConfiguredChatOneShotDefersToolSchemas, with no
// way to opt back in short of predicting the hash. The model then had to
// actively call load_tools for a tool it had no a-priori reason to know
// existed, and typically never did. withMCPServerToolsAlwaysCore
// (tool_tiers.go) fixes this by extending the configured core list with every
// authorized MCP tool before the split, mirroring authorizedAgentTools'
// existing "server selection is the authority" rule.
func TestRunConfiguredChatKeepsGlobalMCPToolInCoreTierEvenWithAConfiguredCoreList(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	fake := &fakeProviderServer{}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)

	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(ws)

	writeTestAgent(t, config.WorkspaceAgentsDir(ws), config.DefaultAgentName, `
name = "`+config.DefaultAgentName+`"
description = "root orchestrator"
`)

	res := &config.Resolved{
		ProviderName: "openrouter",
		Model:        "test/model",
		Models:       []string{"test/model"},
		BaseURL:      server.URL,
		APIKey:       "test-key",
		APIKeyEnv:    "TEST_KEY",
		APIKeySet:    true,
		SystemPrompt: "ROOT PROMPT",
		Subagents:    config.DefaultSubagentConfig,
		// Mirrors .mivia/mivia.toml's [tools] core list shape: a curated,
		// statically-named allowlist. It cannot and does not name the MCP
		// tool - that name does not exist until the server is queried.
		Tools: config.ToolsConfig{Core: &[]string{"read_file", "list_dir", "grep", "glob"}},
		MCP: config.MCPConfig{
			Enabled:                 true,
			StartupTimeoutSeconds:   10,
			MaxServers:              16,
			MaxToolsPerServer:       64,
			MaxToolSchemaBytes:      64 << 10,
			MaxToolDescriptionBytes: 4 << 10,
			MaxToolResultBytes:      64 << 10,
			Servers: []config.MCPServerConfig{{
				ID: "repo", Transport: "stdio", Command: os.Args[0],
				Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: []string{"MIVIA_CLI_MCP_HELPER"},
				Global: true, TimeoutSeconds: 10,
			}},
		},
	}
	res.Subagents.StoreBackend = "sqlite"
	res.Subagents.StorePath = filepath.Join(t.TempDir(), "context.db")
	writeWorkspaceMCPConfig(t, ws, os.Args[0])

	invocation := chatInvocation{prompt: "hello", workspacePath: ws, plainUI: true}
	if err := runConfiguredChat(invocation, res); err != nil {
		t.Fatalf("runConfiguredChat: %v", err)
	}

	requests := fake.advertised()
	if len(requests) == 0 {
		t.Fatal("the stub provider was never called")
	}
	first := requests[0]
	if !slices.Contains(first, "mcp__repo__x6563686f") {
		t.Fatalf("advertised = %v, want the MCP tool advertised directly despite an unrelated configured core list - "+
			"it must never be silently deferred, since its name cannot be predicted and named in that list", first)
	}
	// The configured core tools stay core too - this is additive, not a
	// bypass of the operator's list for anything else.
	for _, want := range []string{"read_file", "list_dir", "grep", "glob"} {
		if !slices.Contains(first, want) {
			t.Fatalf("advertised = %v, want configured core tool %q", first, want)
		}
	}
}

// TestRunConfiguredChatKeepsGlobalMCPToolCoreWithNoAgentDefinitions is the
// end-to-end regression for a workspace with NO agent definitions at all:
// no .agents/agents in the workspace, no user agents under HOME. The session
// runs the root/no-agent-selected identity, whose MCP server selection is the
// config's global server set. Before the fix the tool tier plan consulted
// only a selected agent's EffectiveMCPServers, so the root identity's global
// MCP tool was advertised but silently deferred behind load_tools - whose
// publication can defer again (sibling turns, background orchestration) -
// leaving a configured, successfully-connected server like codegraph a tool
// the model could never actually call. Core placement is proven negatively:
// a core tool never appears in the system prompt's deferred-tool index.
func TestRunConfiguredChatKeepsGlobalMCPToolCoreWithNoAgentDefinitions(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	fake := &fakeProviderServer{}
	server := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(server.Close)

	home := t.TempDir()
	ws := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(ws)
	// Deliberately no writeTestAgent call: no agent named
	// config.DefaultAgentName, no agent passed in the invocation, nothing to
	// select - the root identity is the surface under test.

	res := &config.Resolved{
		ProviderName: "openrouter",
		Model:        "test/model",
		Models:       []string{"test/model"},
		BaseURL:      server.URL,
		APIKey:       "test-key",
		APIKeyEnv:    "TEST_KEY",
		APIKeySet:    true,
		SystemPrompt: "ROOT PROMPT",
		Subagents:    config.DefaultSubagentConfig,
		Tools:        config.ToolsConfig{Core: &[]string{"read_file", "list_dir", "grep", "glob"}},
		MCP: config.MCPConfig{
			Enabled:                 true,
			StartupTimeoutSeconds:   10,
			MaxServers:              16,
			MaxToolsPerServer:       64,
			MaxToolSchemaBytes:      64 << 10,
			MaxToolDescriptionBytes: 4 << 10,
			MaxToolResultBytes:      64 << 10,
			Servers: []config.MCPServerConfig{{
				ID: "repo", Transport: "stdio", Command: os.Args[0],
				Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: []string{"MIVIA_CLI_MCP_HELPER"},
				Global: true, TimeoutSeconds: 10,
			}},
		},
	}
	res.Subagents.StoreBackend = "sqlite"
	res.Subagents.StorePath = filepath.Join(t.TempDir(), "context.db")
	writeWorkspaceMCPConfig(t, ws, os.Args[0])

	invocation := chatInvocation{prompt: "hello", workspacePath: ws, plainUI: true}
	if err := runConfiguredChat(invocation, res); err != nil {
		t.Fatalf("runConfiguredChat: %v", err)
	}

	requests := fake.advertised()
	if len(requests) == 0 {
		t.Fatal("the stub provider was never called")
	}
	if !slices.Contains(requests[0], "mcp__repo__x6563686f") {
		t.Fatalf("advertised = %v, want the global MCP tool advertised to the root identity", requests[0])
	}
	// Core, not deferred: the deferred-tool index lives in the system prompt,
	// so a tool the root identity can call without load_tools must never be
	// named there.
	prompts := fake.systemPrompts()
	if len(prompts) == 0 {
		t.Fatal("no system prompt captured; the deferred-index assertion would be vacuous")
	}
	for _, prompt := range prompts {
		if strings.Contains(prompt, "mcp__repo__x6563686f") {
			t.Fatalf("the system prompt defers the global MCP tool to load_tools; "+
				"the root identity must keep it core (prompt: %.200s...)", prompt)
		}
	}
}

func writeWorkspaceMCPConfig(t *testing.T, ws, helperBinary string) {
	t.Helper()
	path := filepath.Join(ws, ".mivia", "mivia.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `[mcp]
enabled = true

[[mcp.servers]]
id = "repo"
transport = "stdio"
command = "` + tomlPathLiteral(helperBinary) + `"
args = ["-test.run=^TestMCPStdioHelper$"]
env = ["MIVIA_CLI_MCP_HELPER"]
global = true
timeout_seconds = 10
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
