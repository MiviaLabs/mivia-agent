package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"

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
