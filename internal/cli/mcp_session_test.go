package cli

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPStdioHelper serves an MCP server over standard input and output.
// It is the subprocess behind every stdio server in this test file.
// The test binary runs this test when MIVIA_CLI_MCP_HELPER is set.
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

// TestAddMCPToolsRegistersToolsForTurnOne verifies that addMCPTools
// registers the discovered MCP tool under its encoded name.
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

// TestRegisterMCPToolsRejectsUnknownServer verifies that an unknown server
// ID fails registration with an error that names the ID.
func TestRegisterMCPToolsRejectsUnknownServer(t *testing.T) {
	res := serverConfig()
	manager, err := mcp.NewManager(res.MCP, mcp.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	err = registerMCPTools(tools.NewRegistry(), manager, []string{"nope"})
	if err == nil {
		t.Fatal("registerMCPTools() accepted an unknown server")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %q, want mention of %q", err.Error(), "nope")
	}
}

// TestRegisterMCPToolsLogsUnavailableServer verifies that a contained MCP
// server failure is surfaced as an operator warning instead of failing
// registration: the session continues without that server's tools.
func TestRegisterMCPToolsLogsUnavailableServer(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_FAIL", "1")
	res := config.Resolved{MCP: config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "down", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: []string{"MIVIA_CLI_MCP_FAIL"},
		TimeoutSeconds: 10,
	}}}}
	manager, err := mcp.NewManager(res.MCP, mcp.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(original)

	if err := registerMCPTools(tools.NewRegistry(), manager, []string{"down"}); err != nil {
		t.Fatalf("registerMCPTools() error = %v, want a contained server failure", err)
	}
	if !strings.Contains(buf.String(), "down") {
		t.Fatalf("log output = %q, want a warning naming server %q", buf.String(), "down")
	}
}

// TestRegisterMCPToolsPreRegisteredNameRetained verifies the collision guard
// contract. A pre-registered tool name survives MCP registration: the manager
// owns every name it discovers, so the guard skips the name instead of
// erroring, and the pre-registered tool is not replaced by the MCP wrapper.
func TestRegisterMCPToolsPreRegisteredNameRetained(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	res := serverConfig()
	registry := tools.NewRegistry()
	registry.Register(&namedTool{name: "mcp__repo__x6563686f"})
	manager, err := mcp.NewManager(res.MCP, mcp.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := registerMCPTools(registry, manager, []string{"repo"}); err != nil {
		t.Fatalf("registerMCPTools() error = %v, want idempotent skip", err)
	}
	if got := len(registry.List()); got != 1 {
		t.Fatalf("registry.List() length = %d, want 1", got)
	}
	tool, ok := registry.Get("mcp__repo__x6563686f")
	if !ok {
		t.Fatal("registry lost the pre-registered tool name")
	}
	if _, isNamed := tool.(*namedTool); !isNamed {
		t.Fatalf("registry replaced the pre-registered tool with %T", tool)
	}
}

// TestRegisterMCPToolsIsIdempotent verifies that a second registration of
// the same server adds no duplicate tool and returns no error.
func TestRegisterMCPToolsIsIdempotent(t *testing.T) {
	t.Setenv("MIVIA_CLI_MCP_HELPER", "1")
	res := serverConfig()
	registry := tools.NewRegistry()
	cleanupOne, err := addMCPTools(registry, &res, []string{"repo"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupOne()
	cleanupTwo, err := addMCPTools(registry, &res, []string{"repo"})
	if err != nil {
		t.Fatalf("second addMCPTools() error = %v", err)
	}
	defer cleanupTwo()
	if got := len(registry.List()); got != 1 {
		t.Fatalf("registry.List() length = %d, want 1", got)
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
