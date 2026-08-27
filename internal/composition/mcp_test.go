package composition

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPStdioStubHelper serves one MCP tool ("echo") over stdio. It is the
// subprocess behind stubServerConfig, following the same self-reexec idiom
// as internal/mcp/manager_test.go's TestStdioMCPHelper and internal/cli's
// (pre-move) mcp_session_test.go TestMCPStdioHelper: a stdio transport
// spawns os.Args[0] as the server command, so each test binary needs its own
// copy of this helper Test function - there is no way to invoke another
// package's helper across a process boundary. See this slice's report for
// why the pattern is reused, not duplicated as new infrastructure.
func TestMCPStdioStubHelper(t *testing.T) {
	if os.Getenv("MIVIA_COMPOSITION_MCP_FAIL") == "1" {
		// Simulate a server binary that crashes immediately at startup.
		os.Exit(1)
	}
	if os.Getenv("MIVIA_COMPOSITION_MCP_HELPER") != "1" {
		return
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "stub", Version: "1"}, nil)
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

// stubServerConfig builds a working stdio server definition for the "stub"
// server, backed by TestMCPStdioStubHelper.
func stubServerConfig() config.MCPConfig {
	return config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "stub", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestMCPStdioStubHelper$"}, Env: []string{"MIVIA_COMPOSITION_MCP_HELPER"},
		TimeoutSeconds: 10,
	}}}
}

// TestAttachMCPServers_StubServer starts one stdio MCP stub exposing a
// single "echo" tool, attaches it through AttachMCPServers, and asserts the
// registry gained the encoded tool with the expected capability. It then
// asserts that a built-in tool name collision is rejected.
func TestAttachMCPServers_StubServer(t *testing.T) {
	t.Setenv("MIVIA_COMPOSITION_MCP_HELPER", "1")
	cfg := stubServerConfig()
	wantName, err := mcp.EncodeToolName("stub", "echo")
	if err != nil {
		t.Fatalf("EncodeToolName() error = %v", err)
	}

	t.Run("registers the discovered tool", func(t *testing.T) {
		reg := tools.NewRegistry()
		manager, cleanup, err := AttachMCPServers(reg, cfg, nil, []string{"stub"})
		if err != nil {
			t.Fatalf("AttachMCPServers() error = %v", err)
		}
		t.Cleanup(cleanup)
		if manager == nil {
			t.Fatal("AttachMCPServers() returned a nil manager for an enabled config")
		}
		if _, ok := reg.Get(wantName); !ok {
			t.Fatalf("registry.Get(%q) = not found", wantName)
		}
		capability := reg.Capability(wantName, json.RawMessage(`{}`))
		if capability.Class != tools.ExecutionExternal {
			t.Fatalf("capability class = %v, want ExecutionExternal", capability.Class)
		}
		if capability.ResourceKey != "mcp:stub" {
			t.Fatalf("capability resource key = %q, want %q", capability.ResourceKey, "mcp:stub")
		}
	})

	// NOTE (see this slice's report): a genuine collision - reg already
	// holding wrapper.Name() under a tool the manager does NOT own - is not
	// reachable through the public Manager/AttachMCPServers API as it
	// existed pre-move (internal/cli/mcp_session.go, unchanged here).
	// EnsureServers commits every tool it discovers into the manager's own
	// m.tools map before MergeMCPTools ever checks ownership, and that same
	// map is MergeMCPTools's only source for wrapper.Name() values in its
	// check loop - so any name it inspects is, by construction, always
	// already owned by that call's manager. A name pre-registered under the
	// exact string an MCP server will produce is therefore always treated
	// as an idempotent republish, never a collision; this was true before
	// the move (no prior test exercised the "collides with registry" return
	// either) and stays true after it, unchanged. This subtest documents
	// the real, reachable branch: the guard's success path.
	t.Run("pre-registered exact-match name is retained, not replaced", func(t *testing.T) {
		reg := tools.NewRegistry()
		reg.Register(&stubBuiltinTool{name: wantName})
		manager, cleanup, err := AttachMCPServers(reg, cfg, nil, []string{"stub"})
		if err != nil {
			t.Fatalf("AttachMCPServers() error = %v, want idempotent skip", err)
		}
		t.Cleanup(cleanup)
		if manager == nil {
			t.Fatal("AttachMCPServers() returned a nil manager for an enabled config")
		}
		tool, ok := reg.Get(wantName)
		if !ok {
			t.Fatal("registry lost the pre-registered tool name")
		}
		if _, isStub := tool.(*stubBuiltinTool); !isStub {
			t.Fatalf("registry replaced the pre-registered tool with %T", tool)
		}
	})
}

// TestAttachMCPServersDisabledConfigIsNoOp verifies that a disabled MCP
// configuration attaches no tools, returns a nil manager, and no error.
func TestAttachMCPServersDisabledConfigIsNoOp(t *testing.T) {
	reg := tools.NewRegistry()
	manager, cleanup, err := AttachMCPServers(reg, config.MCPConfig{Enabled: false}, nil, []string{"stub"})
	if err != nil {
		t.Fatalf("AttachMCPServers() error = %v", err)
	}
	cleanup()
	if manager != nil {
		t.Fatal("AttachMCPServers() returned a non-nil manager for a disabled config")
	}
	if got := len(reg.List()); got != 0 {
		t.Fatalf("registry.List() length = %d, want 0", got)
	}
}

// TestMergeMCPToolsRejectsUnknownServer verifies that an unknown server ID
// fails the merge with an error naming the ID.
func TestMergeMCPToolsRejectsUnknownServer(t *testing.T) {
	t.Setenv("MIVIA_COMPOSITION_MCP_HELPER", "1")
	manager, err := mcp.NewManager(stubServerConfig(), mcp.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	err = MergeMCPTools(tools.NewRegistry(), manager, []string{"nope"})
	if err == nil {
		t.Fatal("MergeMCPTools() accepted an unknown server")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %q, want mention of %q", err.Error(), "nope")
	}
}

// TestMergeMCPToolsLogsUnavailableServer verifies that a contained MCP
// server failure is surfaced as an operator warning instead of failing the
// merge: the caller continues without that server's tools.
func TestMergeMCPToolsLogsUnavailableServer(t *testing.T) {
	t.Setenv("MIVIA_COMPOSITION_MCP_FAIL", "1")
	cfg := config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "down", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestMCPStdioStubHelper$"}, Env: []string{"MIVIA_COMPOSITION_MCP_FAIL"},
		TimeoutSeconds: 10,
	}}}
	manager, err := mcp.NewManager(cfg, mcp.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	var buf bytes.Buffer
	original := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(original)

	if err := MergeMCPTools(tools.NewRegistry(), manager, []string{"down"}); err != nil {
		t.Fatalf("MergeMCPTools() error = %v, want a contained server failure", err)
	}
	if !strings.Contains(buf.String(), "down") {
		t.Fatalf("log output = %q, want a warning naming server %q", buf.String(), "down")
	}
}

// TestAttachMCPServersIsIdempotent verifies that a second attach of the same
// server adds no duplicate tool and returns no error.
func TestAttachMCPServersIsIdempotent(t *testing.T) {
	t.Setenv("MIVIA_COMPOSITION_MCP_HELPER", "1")
	cfg := stubServerConfig()
	reg := tools.NewRegistry()
	_, cleanupOne, err := AttachMCPServers(reg, cfg, nil, []string{"stub"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupOne()
	_, cleanupTwo, err := AttachMCPServers(reg, cfg, nil, []string{"stub"})
	if err != nil {
		t.Fatalf("second AttachMCPServers() error = %v", err)
	}
	defer cleanupTwo()
	if got := len(reg.List()); got != 1 {
		t.Fatalf("registry.List() length = %d, want 1", got)
	}
}

// stubBuiltinTool is a minimal tools.Tool used to occupy a name in advance
// of a server merge, exercising the idempotent-retain path (see the NOTE on
// TestAttachMCPServers_StubServer for why a true collision is unreachable).
type stubBuiltinTool struct{ name string }

func (t *stubBuiltinTool) Name() string               { return t.name }
func (t *stubBuiltinTool) Description() string        { return t.name }
func (t *stubBuiltinTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *stubBuiltinTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "ok", nil
}
