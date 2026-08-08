package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
)

func TestManagerEnsuresOnlyRequestedServerOnce(t *testing.T) {
	var connects atomic.Int32
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: "/bin/echo",
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		connects.Add(1)
		return fakeRemoteClient{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureServers(context.Background(), []string{"repository"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureServers(context.Background(), []string{"repository"}); err != nil {
		t.Fatal(err)
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("connect count = %d, want 1", got)
	}
}

func TestSameOriginRedirectRefusesCrossOrigin(t *testing.T) {
	endpoint, err := url.Parse("https://example.test/mcp")
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, "https://other.test/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sameOriginRedirect(endpoint)(request, nil); err != http.ErrUseLastResponse {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
}

func TestManagerRejectsMoreToolsThanConfigured(t *testing.T) {
	m, err := NewManager(config.MCPConfig{Enabled: true, MaxToolsPerServer: 1, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: "/bin/echo",
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		return toolListClient{tools: []remoteTool{{Name: "one"}, {Name: "two"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureServers(context.Background(), []string{"repository"}); err == nil {
		t.Fatal("EnsureServers() accepted too many remote tools")
	}
}

func TestManagerMemoizesFailedDiscovery(t *testing.T) {
	var connects atomic.Int32
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: "/bin/echo",
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		connects.Add(1)
		return nil, errors.New("dial failure")
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := m.EnsureServers(context.Background(), []string{"repository"}); err == nil {
			t.Fatal("EnsureServers() accepted a failed server")
		}
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("connect count = %d, want 1 after failure", got)
	}
}

func TestManagerAppliesServerTimeout(t *testing.T) {
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: "/bin/echo", TimeoutSeconds: 1,
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		return fakeRemoteClient{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := m.serverContext(context.Background(), config.MCPServerConfig{TimeoutSeconds: 1})
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
		t.Fatalf("server context deadline = %v, ok = %v", deadline, ok)
	}
}

func TestManagerOwnsDiscoveredTool(t *testing.T) {
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: "/bin/echo",
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		return toolListClient{tools: []remoteTool{{Name: "read"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	wrappers, err := m.EnsureServers(context.Background(), []string{"repository"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.OwnsTool(wrappers[0].Name()) {
		t.Fatalf("OwnsTool(%q) = false", wrappers[0].Name())
	}
}

func TestNewHTTPClientUsesBoundedTransport(t *testing.T) {
	endpoint, err := url.Parse("https://example.test/mcp")
	if err != nil {
		t.Fatal(err)
	}
	client := newHTTPClient(http.Header{}, endpoint)
	wrapped, ok := client.Transport.(headerTransport)
	if !ok {
		t.Fatalf("transport = %T, want headerTransport", client.Transport)
	}
	transport, ok := wrapped.base.(*http.Transport)
	if !ok || transport.TLSHandshakeTimeout == 0 || transport.ResponseHeaderTimeout == 0 || transport.IdleConnTimeout == 0 {
		t.Fatalf("HTTP transport bounds = %#v", transport)
	}
}

func TestSerializedRemoteClientSerializesCalls(t *testing.T) {
	client := &serialProbeClient{entered: make(chan struct{}, 2), release: make(chan struct{})}
	wrapped := &serializedRemoteClient{client: client}
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			_, _ = wrapped.CallTool(context.Background(), "tool", nil)
			done <- struct{}{}
		}()
	}
	<-client.entered
	select {
	case <-client.entered:
		t.Fatal("serialized client started two calls at once")
	case <-time.After(20 * time.Millisecond):
	}
	client.release <- struct{}{}
	<-client.entered
	client.release <- struct{}{}
	<-done
	<-done
}

type fakeRemoteClient struct{}

func (fakeRemoteClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (fakeRemoteClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", nil
}
func (fakeRemoteClient) Close() error { return nil }

type toolListClient struct{ tools []remoteTool }

func (c toolListClient) ListTools(context.Context) ([]remoteTool, error) { return c.tools, nil }
func (toolListClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", nil
}
func (toolListClient) Close() error { return nil }

type serialProbeClient struct {
	entered chan struct{}
	release chan struct{}
}

func (c *serialProbeClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (c *serialProbeClient) CallTool(context.Context, string, map[string]any) (string, error) {
	c.entered <- struct{}{}
	<-c.release
	return "", nil
}
func (c *serialProbeClient) Close() error { return nil }
