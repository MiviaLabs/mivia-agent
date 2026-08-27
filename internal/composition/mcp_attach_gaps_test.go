package composition

// mcp_attach_gaps_test.go covers AttachMCPServers' NewMCPManager-failure
// branch: a config with duplicate server ids makes the manager constructor
// fail closed, and the attach must surface that error without leaking a
// manager.

import (
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestAttachMCPServersManagerConstructionFailure(t *testing.T) {
	// os.Args[0] is an existing executable regular file on every GOOS, so
	// ValidateServerConfig passes on entry #1 and the loop reaches entry #2,
	// where the duplicate-id check fires. "/bin/echo" only exists on Unix.
	dupCmd := os.Args[0]
	cfg := config.MCPConfig{
		Enabled: true,
		Servers: []config.MCPServerConfig{
			{ID: "dup", Transport: "stdio", Command: dupCmd}, {ID: "dup", Transport: "stdio", Command: dupCmd},
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
