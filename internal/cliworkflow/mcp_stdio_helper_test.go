package cliworkflow

// mcp_stdio_helper_test.go duplicates cli's stdio MCP test subprocess helper
// (test_helpers_moved_test.go): a stdio transport spawns os.Args[0] as the
// server command, and this package's test binary needs its own copy.

import (
	"context"
	"os"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverConfig returns a resolved config with one stdio MCP server that
// spawns the test binary itself as the server subprocess.
func serverConfig() config.Resolved {
	return config.Resolved{MCP: config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repo", Transport: "stdio", Command: os.Args[0],
		Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: []string{"MIVIA_CLI_MCP_HELPER"},
		TimeoutSeconds: 10,
	}}}}
}

// TestMCPStdioHelper serves an MCP server over standard input and output.
// It is the subprocess behind workflow_mcp_test.go's stdio MCP stub.
func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("MIVIA_CLI_MCP_FAIL") == "1" {
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
