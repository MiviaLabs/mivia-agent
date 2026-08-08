package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type remoteTool struct {
	Name        string
	Description string
	Schema      map[string]any
}

type remoteClient interface {
	ListTools(context.Context) ([]remoteTool, error)
	CallTool(context.Context, string, map[string]any) (string, error)
	Close() error
}

type sdkClient struct {
	session *sdk.ClientSession
	command *exec.Cmd
}

func (c *sdkClient) ListTools(ctx context.Context) ([]remoteTool, error) {
	result, err := c.session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]remoteTool, 0, len(result.Tools))
	for _, tool := range result.Tools {
		schema, _ := tool.InputSchema.(map[string]any)
		out = append(out, remoteTool{Name: tool.Name, Description: tool.Description, Schema: schema})
	}
	return out, nil
}
func (c *sdkClient) CallTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	result, err := c.session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(result.Content)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
func (c *sdkClient) Close() error {
	err := c.session.Close()
	if c.command == nil {
		return err
	}
	waitErr := c.command.Wait()
	if err != nil {
		return err
	}
	return waitErr
}

func connectServer(ctx context.Context, server config.MCPServerConfig) (remoteClient, error) {
	if server.Transport == "stdio" {
		return connectStdio(ctx, server)
	}
	return connectStreamableHTTP(ctx, server)
}

func connectStreamableHTTP(ctx context.Context, server config.MCPServerConfig) (remoteClient, error) {
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header, len(server.Headers))
	for _, header := range server.Headers {
		if value, ok := os.LookupEnv(header.ValueEnv); ok {
			headers.Set(header.Name, value)
		}
	}
	client := &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: headers}, CheckRedirect: sameOriginRedirect(endpoint)}
	session, err := sdk.NewClient(&sdk.Implementation{Name: "mivia", Version: "1"}, nil).Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.URL, HTTPClient: client, MaxRetries: 1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return nil, err
	}
	return &sdkClient{session: session}, nil
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	for name, values := range t.headers {
		clone.Header.Del(name)
		for _, value := range values {
			clone.Header.Add(name, value)
		}
	}
	return t.base.RoundTrip(clone)
}

func sameOriginRedirect(endpoint *url.URL) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, _ []*http.Request) error {
		if request.URL.Scheme != endpoint.Scheme || request.URL.Host != endpoint.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}
}

func connectStdio(ctx context.Context, server config.MCPServerConfig) (remoteClient, error) {
	command := exec.CommandContext(ctx, server.Command, server.Args...)
	command.Env = environmentFor(server.Env)
	in, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	session, err := sdk.NewClient(&sdk.Implementation{Name: "mivia", Version: "1"}, nil).Connect(ctx, &sdk.IOTransport{Reader: out, Writer: in}, nil)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, err
	}
	return &sdkClient{session: session, command: command}, nil
}

func environmentFor(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if value, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+value)
		}
	}
	return out
}

// ManagerOptions supplies the transport constructor. Production wiring uses
// the MCP SDK constructor. Tests provide a deterministic fake.
type ManagerOptions struct {
	Connect func(context.Context, config.MCPServerConfig) (remoteClient, error)
}

// Manager owns one lazy client per configured MCP server.
type Manager struct {
	cfg     config.MCPConfig
	connect func(context.Context, config.MCPServerConfig) (remoteClient, error)
	mu      sync.Mutex
	clients map[string]remoteClient
	tools   map[string][]tools.Tool
	closed  bool
}

// NewManager constructs a disconnected MCP manager.
func NewManager(cfg config.MCPConfig, opts ManagerOptions) (*Manager, error) {
	if !cfg.Enabled {
		return &Manager{cfg: cfg, clients: map[string]remoteClient{}, tools: map[string][]tools.Tool{}}, nil
	}
	if opts.Connect == nil {
		opts.Connect = connectServer
	}
	for _, server := range cfg.Servers {
		if err := ValidateServerConfig(server); err != nil {
			return nil, err
		}
	}
	return &Manager{cfg: cfg, connect: opts.Connect, clients: map[string]remoteClient{}, tools: map[string][]tools.Tool{}}, nil
}

// EnsureServers discovers tools for the requested server IDs once per session.
func (m *Manager) EnsureServers(ctx context.Context, ids []string) ([]tools.Tool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fmt.Errorf("MCP manager is closed")
	}
	var out []tools.Tool
	for _, id := range ids {
		if !m.cfg.Enabled {
			return nil, fmt.Errorf("MCP is disabled")
		}
		server, ok := m.server(id)
		if !ok {
			return nil, fmt.Errorf("unknown MCP server %q", id)
		}
		if _, ok := m.clients[id]; !ok {
			client, err := m.connect(ctx, server)
			if err != nil {
				return nil, fmt.Errorf("connect MCP server %q: %w", id, err)
			}
			m.clients[id] = client
			remote, err := client.ListTools(ctx)
			if err != nil {
				_ = client.Close()
				delete(m.clients, id)
				return nil, fmt.Errorf("discover MCP server %q: %w", id, err)
			}
			wrapped, err := wrapRemoteTools(id, client, remote)
			if err != nil {
				_ = client.Close()
				delete(m.clients, id)
				return nil, err
			}
			m.tools[id] = wrapped
		}
		out = append(out, m.tools[id]...)
	}
	return out, nil
}

func (m *Manager) server(id string) (config.MCPServerConfig, bool) {
	for _, server := range m.cfg.Servers {
		if server.ID == id {
			return server, true
		}
	}
	return config.MCPServerConfig{}, false
}

// Close closes every connected MCP client.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	var first error
	for _, client := range m.clients {
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
