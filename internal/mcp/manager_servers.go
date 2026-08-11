package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// EnsureServers discovers tools for the requested server IDs once per session.
//
// A server that fails to connect or to be discovered is contained: its failure
// is recorded and the other requested servers still get their tools. A server
// outage must never fail a session or a workflow start; the caller receives
// the tools of the healthy servers. Structural errors - a closed manager, MCP
// disabled, or an unknown server ID - still fail, because they are caller or
// configuration bugs, not server outages.
func (m *Manager) EnsureServers(ctx context.Context, ids []string) ([]tools.Tool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("MCP manager is closed")
	}
	if !m.cfg.Enabled {
		return nil, fmt.Errorf("MCP is disabled")
	}
	var out []tools.Tool
	for _, id := range ids {
		server, ok := m.server(id)
		if !ok {
			return nil, fmt.Errorf("unknown MCP server %q", id)
		}
		if _, failed := m.failures[id]; failed {
			continue
		}
		if _, ok := m.clients[id]; !ok {
			startupCtx, cancel := m.startupContext(ctx)
			client, err := m.connect(startupCtx, server)
			cancel()
			if err != nil {
				m.failures[id] = err
				continue
			}
			client = &serializedRemoteClient{client: client}
			m.clients[id] = client
			discoveryCtx, cancel := m.serverContext(ctx, server)
			remote, err := client.ListTools(discoveryCtx)
			cancel()
			if err != nil {
				_ = client.Close()
				delete(m.clients, id)
				m.failures[id] = err
				continue
			}
			if m.cfg.MaxToolsPerServer > 0 && len(remote) > m.cfg.MaxToolsPerServer {
				_ = client.Close()
				delete(m.clients, id)
				m.failures[id] = fmt.Errorf("too many tools")
				continue
			}
			wrapped, err := wrapRemoteTools(id, client, remote, m.cfg.MaxToolDescriptionBytes, m.cfg.MaxToolSchemaBytes, m.maxResultBytes, server.TimeoutSeconds, m.redaction)
			if err != nil {
				_ = client.Close()
				delete(m.clients, id)
				m.failures[id] = err
				continue
			}
			m.tools[id] = wrapped
		}
		out = append(out, m.tools[id]...)
	}
	return out, nil
}

func (m *Manager) startupContext(parent context.Context) (context.Context, context.CancelFunc) {
	return m.boundContext(parent, m.cfg.StartupTimeoutSeconds)
}

func (m *Manager) serverContext(parent context.Context, server config.MCPServerConfig) (context.Context, context.CancelFunc) {
	return m.boundContext(parent, server.TimeoutSeconds)
}

func (m *Manager) boundContext(parent context.Context, timeoutSeconds int) (context.Context, context.CancelFunc) {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeoutSeconds <= 0 {
		ctx, cancel = context.WithCancel(parent)
	} else {
		ctx, cancel = context.WithTimeout(parent, time.Duration(timeoutSeconds)*time.Second)
	}
	stop := context.AfterFunc(m.shutdownCtx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (m *Manager) server(id string) (config.MCPServerConfig, bool) {
	for _, server := range m.cfg.Servers {
		if server.ID == id {
			return server, true
		}
	}
	return config.MCPServerConfig{}, false
}
