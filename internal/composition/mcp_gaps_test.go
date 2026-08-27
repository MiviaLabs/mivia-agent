package composition

// mcp_gaps_test.go covers the remaining error branches of NewMCPManager and
// AttachMCPServers: an invalid enabled MCP configuration, and a merge failure
// that must close the manager the attach created.

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestNewMCPManagerRejectsDuplicateServers(t *testing.T) {
	base := stubServerConfig()
	dup := base
	dup.Servers = append([]config.MCPServerConfig(nil), base.Servers...)
	dup.Servers = append(dup.Servers, base.Servers[0])
	manager, err := NewMCPManager(dup, nil)
	if err == nil || manager != nil {
		t.Fatalf("NewMCPManager(duplicate server) = (%v, %v), want (nil, error)", manager, err)
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("error = %v, want the duplicate-server failure", err)
	}
}

func TestAttachMCPServersClosesManagerOnMergeError(t *testing.T) {
	// An enabled manager with an unknown server ID fails the merge, so the
	// attach must return the error and not leak the manager it built.
	cfg := stubServerConfig()
	manager, cleanup, err := AttachMCPServers(tools.NewRegistry(), cfg, nil, []string{"no-such-server"})
	if err == nil || manager != nil {
		t.Fatalf("AttachMCPServers(unknown server) = (manager %v, err %v), want (nil, error)", manager, err)
	}
	if cleanup != nil {
		t.Fatal("AttachMCPServers must not return a cleanup after a failed merge")
	}
	if !strings.Contains(err.Error(), "no-such-server") {
		t.Fatalf("error = %v, want it to name the unknown server", err)
	}
}
