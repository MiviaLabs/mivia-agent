package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPStdioHelper serves an MCP server over standard input and output.
// It is the subprocess behind every stdio server in this package's tests
// (mcp_session_test.go, chat_mcp_entrypoint_integration_test.go,
// workflow_mcp_test.go): a stdio transport spawns os.Args[0] as the server
// command, so every test in this test binary that needs a stdio MCP stub
// reuses this one helper. See internal/composition/mcp_test.go's
// TestMCPStdioStubHelper for why composition needs its own equivalent copy
// (a different test binary cannot invoke this package's helper).
func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("MIVIA_CLI_MCP_FAIL") == "1" {
		// Simulate a server binary that crashes immediately at startup.
		os.Exit(1)
	}
	if os.Getenv("MIVIA_CLI_MCP_HELPER") != "1" {
		return
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "returns text"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "reply"}}}, nil, nil
	})
	session, err := server.Connect(context.Background(), &sdk.IOTransport{Reader: os.Stdin, Writer: os.Stdout}, nil)
	if err != nil {
		os.Exit(2)
	}
	if err := session.Wait(); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

// serverConfig builds a working stdio server definition for the helper test.
func serverConfig() config.Resolved {
	return config.Resolved{MCP: config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repo", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: []string{"MIVIA_CLI_MCP_HELPER"},
		TimeoutSeconds: 10,
	}}}}
}

// TestAddMCPToolsRegistersToolsForTurnOne verifies that the cli-level
// addMCPTools wrapper still registers the discovered MCP tool under its
// encoded name, now via composition.AttachMCPServers.
func TestAddMCPToolsRegistersToolsForTurnOne(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	res := serverConfig()
	registry := tools.NewRegistry()
	cleanup, err := addMCPTools(registry, &res, []string{"repo"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	tool, ok := registry.Get("mcp__repo__x6563686f")
	if !ok {
		t.Fatalf("registry.Get(%q) = not found", "mcp__repo__x6563686f")
	}
	description := tool.Description()
	if description == "" || !strings.Contains(description, "server") || !strings.Contains(description, "echo") {
		t.Fatalf("tool description = %q, want mentions of server and echo", description)
	}
	var found bool
	for _, entry := range registry.OpenAITools() {
		fn, _ := entry["function"].(map[string]any)
		if name, _ := fn["name"].(string); name == "mcp__repo__x6563686f" {
			found = true
		}
	}
	if !found {
		t.Fatal("OpenAITools() does not expose mcp__repo__x6563686f")
	}
}

// TestAddMCPToolsDisabledConfigIsNoOp verifies that a disabled MCP
// configuration adds no tools and returns no error.
func TestAddMCPToolsDisabledConfigIsNoOp(t *testing.T) {
	res := config.Resolved{MCP: config.MCPConfig{Enabled: false}}
	registry := tools.NewRegistry()
	cleanup, err := addMCPTools(registry, &res, []string{"repo"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got := len(registry.List()); got != 0 {
		t.Fatalf("registry.List() length = %d, want 0", got)
	}
}

// TestSetupSessionMCPToolsRegistersSelectedServer verifies that the session
// setup registers the selected agent's MCP servers and returns a manager.
func TestSetupSessionMCPToolsRegistersSelectedServer(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	selected := &agents.ResolvedAgent{EffectiveMCPServers: []string{"repo"}}
	res := serverConfig()
	registry := tools.NewRegistry()
	manager, cleanup, err := setupSessionMCPTools(registry, &res, selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("mcp__repo__x6563686f"); !ok {
		t.Fatal("registry.Get(mcp__repo__x6563686f) = not found")
	}
	cleanup()
	if manager == nil {
		t.Fatal("setupSessionMCPTools() returned a nil manager")
	}
}

// TestEnsureSelectedMCPToolsRegistersServer verifies that ensureSelectedMCPTools
// (used by /agent switch) merges the selected agent's MCP servers into the
// session's borrowed manager, now via composition.MergeMCPTools.
func TestEnsureSelectedMCPToolsRegistersServer(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	res := serverConfig()
	registry := tools.NewRegistry()
	manager, cleanup, err := setupSessionMCPTools(registry, &res, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	state := &AgentSessionState{ToolBase: registry, MCPManager: manager}
	selected := agents.ResolvedAgent{EffectiveMCPServers: []string{"repo"}}
	if err := ensureSelectedMCPTools(state, selected); err != nil {
		t.Fatalf("ensureSelectedMCPTools() error = %v", err)
	}
	if _, ok := registry.Get("mcp__repo__x6563686f"); !ok {
		t.Fatal("registry.Get(mcp__repo__x6563686f) = not found")
	}
}
