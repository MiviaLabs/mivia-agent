package composition

// mcp_attach_gaps_test.go covers AttachMCPServers' NewMCPManager-failure
// branch: a config with duplicate server ids makes the manager constructor
// fail closed, and the attach must surface that error without leaking a
// manager.

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestAttachMCPServersManagerConstructionFailure(t *testing.T) {
	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{ID: "dup", Transport: "stdio", Command: "/bin/echo"}, {ID: "dup", Transport: "stdio", Command: "/bin/echo"},
		},
	}
	manager, cleanup, err := AttachMCPServers(nil, cfg, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate MCP server") {
		t.Fatalf("AttachMCPServers(duplicate ids) err = %v, want duplicate-server failure", err)
	}
	if manager != nil || cleanup != nil {
		t.Fatalf("AttachMCPServers failure must return nil manager/cleanup; got %v %v", manager, cleanup != nil)
	}
}
