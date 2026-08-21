package composition

import (
	"context"
	"fmt"
	"log"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/mcp"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// NewMCPManager builds an MCP manager from cfg. It returns (nil, nil) when
// cfg.Enabled is false, matching the historical cli behavior: a disabled MCP
// configuration is a no-op, not an error.
func NewMCPManager(cfg config.MCPConfig, redactionPolicy *redact.Policy) (*mcp.Manager, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	return mcp.NewManager(cfg, mcp.ManagerOptions{RedactionPolicy: redactionPolicy})
}

// AttachMCPServers builds an MCP manager for cfg and merges serverIDs' tools
// into reg (see MergeMCPTools). It returns the manager (nil when MCP is
// disabled) and a cleanup that closes it. On error the manager, if created,
// is closed before returning so a failed attach leaks no client.
func AttachMCPServers(reg *tools.Registry, cfg config.MCPConfig, redactionPolicy *redact.Policy, serverIDs []string) (*mcp.Manager, func(), error) {
	manager, err := NewMCPManager(cfg, redactionPolicy)
	if err != nil {
		return nil, nil, err
	}
	if manager == nil {
		return nil, func() {}, nil
	}
	if err := MergeMCPTools(reg, manager, serverIDs); err != nil {
		_ = manager.Close()
		return nil, nil, err
	}
	return manager, func() { _ = manager.Close() }, nil
}

// MergeMCPTools discovers each of serverIDs' tools through manager and
// registers them into reg under the manager's mcp__<server>__<tool> name
// encoding (see internal/mcp's discoveredTool.Name). A contained server
// outage never fails the merge; the session continues without that server's
// tools, and the operator is warned by server ID only - external error text
// can carry request content (DC-14). A discovered tool whose name collides
// with a name reg already holds is rejected as an error unless manager owns
// that name (an idempotent re-merge of the same server).
func MergeMCPTools(reg *tools.Registry, manager *mcp.Manager, serverIDs []string) error {
	if reg == nil || manager == nil || len(serverIDs) == 0 {
		return nil
	}
	wrappers, err := manager.EnsureServers(context.Background(), serverIDs)
	if err != nil {
		return err
	}
	for id := range manager.Failures() {
		log.Printf("mcp: server %q is unavailable; its tools are not registered", id)
	}
	for _, wrapper := range wrappers {
		if _, exists := reg.Get(wrapper.Name()); exists {
			if manager.OwnsTool(wrapper.Name()) {
				continue
			}
			return fmt.Errorf("MCP tool %q collides with registry", wrapper.Name())
		}
	}
	for _, wrapper := range wrappers {
		if _, exists := reg.Get(wrapper.Name()); exists {
			continue
		}
		reg.Register(wrapper)
	}
	return nil
}
