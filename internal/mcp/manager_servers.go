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
//
// Concurrent calls for the same server ID single-flight on a per-server
// pending claim, so each server is connected and discovered once per session.
// The manager mutex is held only for map bookkeeping: connect, ListTools, and
// tool wrapping run without it, so Failures, OwnsTool, Close, and concurrent
// EnsureServers calls never block behind a network round trip.
func (m *Manager) EnsureServers(ctx context.Context, ids []string) ([]tools.Tool, error) {
	type ensureRequest struct {
		id     string
		server config.MCPServerConfig
		done   chan struct{}
		owner  bool
	}
	var requests []ensureRequest
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("MCP manager is closed")
	}
	if !m.cfg.Enabled {
		m.mu.Unlock()
		return nil, fmt.Errorf("MCP is disabled")
	}
	// Pre-flight structural validation runs before any claim exists, so a
	// refusal can never orphan a pending entry.
	for _, id := range ids {
		if _, ok := m.server(id); !ok {
			m.mu.Unlock()
			return nil, fmt.Errorf("unknown MCP server %q", id)
		}
	}
	// Classify under the lock: failed ids are skipped, connected ids need no
	// claim, an existing pending entry makes this caller a waiter, and
	// otherwise this caller owns the claim.
	for _, id := range ids {
		server, _ := m.server(id)
		if _, failed := m.failures[id]; failed {
			continue
		}
		if _, ok := m.clients[id]; ok {
			continue
		}
		if done, ok := m.pending[id]; ok {
			requests = append(requests, ensureRequest{id: id, server: server, done: done})
			continue
		}
		done := make(chan struct{})
		m.pending[id] = done
		requests = append(requests, ensureRequest{id: id, server: server, done: done, owner: true})
	}
	m.mu.Unlock()

	// Owners run before waiters so a duplicate id inside one call cannot
	// deadlock on its own pending claim.
	for _, request := range requests {
		if request.owner {
			m.connectOne(ctx, request.id, request.server, request.done)
		}
	}
	// Waiters block until the owning call commits and releases its claim. The
	// collect phase below derives its answer from committed state, so a waiter
	// that gives up on a canceled context still returns whatever the owner
	// managed to commit (or none, contained), never an error.
	for _, request := range requests {
		if request.owner {
			continue
		}
		select {
		case <-request.done:
		case <-ctx.Done():
		case <-m.shutdownCtx.Done():
		}
	}

	var out []tools.Tool
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if _, failed := m.failures[id]; failed {
			continue
		}
		out = append(out, m.tools[id]...)
	}
	return out, nil
}

// connectOne connects and discovers one server without holding m.mu. Every
// branch - connect failure, discovery failure, too many tools, a wrapping
// failure, a manager closed while in flight, and success - commits under a
// brief re-lock that records the outcome, releases the pending claim, and
// closes done exactly once, so waiters never block on a stuck claim.
func (m *Manager) connectOne(ctx context.Context, id string, server config.MCPServerConfig, done chan struct{}) {
	startupCtx, cancel := m.startupContext(ctx)
	client, err := m.connect(startupCtx, server)
	cancel()
	if err != nil {
		m.commitClaim(id, nil, nil, err, done)
		return
	}
	client = &serializedRemoteClient{client: client}
	discoveryCtx, cancel := m.serverContext(ctx, server)
	remote, err := client.ListTools(discoveryCtx)
	cancel()
	if err != nil {
		_ = client.Close()
		m.commitClaim(id, nil, nil, err, done)
		return
	}
	if m.cfg.MaxToolsPerServer > 0 && len(remote) > m.cfg.MaxToolsPerServer {
		_ = client.Close()
		m.commitClaim(id, nil, nil, fmt.Errorf("too many tools"), done)
		return
	}
	wrapped, err := wrapRemoteTools(id, client, remote, m.cfg.MaxToolDescriptionBytes, m.cfg.MaxToolSchemaBytes, m.maxResultBytes, server.TimeoutSeconds, m.redaction)
	if err != nil {
		_ = client.Close()
		m.commitClaim(id, nil, nil, err, done)
		return
	}
	m.commitClaim(id, client, wrapped, nil, done)
}

// commitClaim records one server's outcome under the lock and releases its
// pending claim. A client that was connected while the manager closed
// mid-flight is closed here rather than stored, so an in-flight connect cannot
// leak a live client.
func (m *Manager) commitClaim(id string, client remoteClient, wrapped []tools.Tool, failErr error, done chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case failErr != nil:
		if client != nil {
			_ = client.Close()
		}
		m.failures[id] = failErr
	case m.closed:
		if client != nil {
			_ = client.Close()
		}
	default:
		m.clients[id] = client
		m.tools[id] = wrapped
	}
	delete(m.pending, id)
	close(done)
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
