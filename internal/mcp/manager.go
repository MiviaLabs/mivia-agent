package mcp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpShutdownTimeout = 5 * time.Second

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
	session    *sdk.ClientSession
	command    *exec.Cmd
	httpClient *http.Client
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
	if result.IsError {
		return "", fmt.Errorf("MCP tool returned an error")
	}
	parts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		if text, ok := content.(*sdk.TextContent); ok {
			parts = append(parts, text.Text)
			continue
		}
		parts = append(parts, "[unsupported MCP result content]")
	}
	return strings.Join(parts, "\n"), nil
}
func (c *sdkClient) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	sessionDone := make(chan error, 1)
	go func() { sessionDone <- c.session.Close() }()
	if c.command == nil {
		select {
		case err := <-sessionDone:
			return err
		case <-time.After(mcpShutdownTimeout):
			return fmt.Errorf("MCP client shutdown timed out")
		}
	}
	commandDone := make(chan error, 1)
	go func() { commandDone <- c.command.Wait() }()
	timer := time.NewTimer(mcpShutdownTimeout)
	defer timer.Stop()
	select {
	case sessionErr := <-sessionDone:
		select {
		case waitErr := <-commandDone:
			if sessionErr != nil {
				return sessionErr
			}
			return waitErr
		case <-timer.C:
			_ = c.command.Process.Kill()
			waitErr := <-commandDone
			if sessionErr != nil {
				return sessionErr
			}
			return waitErr
		}
	case <-timer.C:
		_ = c.command.Process.Kill()
		<-commandDone
		select {
		case sessionErr := <-sessionDone:
			return sessionErr
		case <-time.After(mcpShutdownTimeout):
			return fmt.Errorf("MCP client shutdown timed out")
		}
	}
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
	client := newHTTPClient(headers, endpoint)
	session, err := sdk.NewClient(&sdk.Implementation{Name: "mivia", Version: "1"}, nil).Connect(ctx, &sdk.StreamableClientTransport{Endpoint: server.URL, HTTPClient: client, MaxRetries: 1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return nil, err
	}
	return &sdkClient{session: session, httpClient: client}, nil
}

func newHTTPClient(headers http.Header, endpoint *url.URL) *http.Client {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   1,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Transport: headerTransport{base: transport, headers: headers}, CheckRedirect: sameOriginRedirect(endpoint)}
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

type redirectRequestKey struct{}

type serializedRemoteClient struct {
	client       remoteClient
	operationMu  sync.Mutex
	stateMu      sync.Mutex
	activeCancel context.CancelFunc
	closed       bool
	closeDone    chan struct{}
	closeErr     error
}

func (c *serializedRemoteClient) ListTools(ctx context.Context) ([]remoteTool, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.stateMu.Lock()
	closed := c.closed
	c.stateMu.Unlock()
	if closed {
		return nil, fmt.Errorf("MCP client is closed")
	}
	return c.client.ListTools(ctx)
}

func (c *serializedRemoteClient) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	callCtx, cancel, err := c.startCall(ctx)
	if err != nil {
		return "", err
	}
	defer c.finishCall(cancel)
	return c.client.CallTool(callCtx, name, args)
}

func (c *serializedRemoteClient) Close() error {
	c.stateMu.Lock()
	c.closed = true
	if c.activeCancel != nil {
		c.activeCancel()
	}
	if c.closeDone == nil {
		c.closeDone = make(chan struct{})
		go func() {
			err := c.client.Close()
			c.stateMu.Lock()
			c.closeErr = err
			c.stateMu.Unlock()
			close(c.closeDone)
		}()
	}
	closeDone := c.closeDone
	c.stateMu.Unlock()
	select {
	case <-closeDone:
		c.stateMu.Lock()
		err := c.closeErr
		c.stateMu.Unlock()
		return err
	case <-time.After(mcpShutdownTimeout):
		return fmt.Errorf("MCP client shutdown timed out")
	}
}

func (c *serializedRemoteClient) startCall(ctx context.Context) (context.Context, context.CancelFunc, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.closed {
		return nil, nil, fmt.Errorf("MCP client is closed")
	}
	callCtx, cancel := context.WithCancel(ctx)
	c.activeCancel = cancel
	return callCtx, cancel, nil
}

func (c *serializedRemoteClient) finishCall(cancel context.CancelFunc) {
	c.stateMu.Lock()
	if c.activeCancel != nil {
		c.activeCancel = nil
	}
	c.stateMu.Unlock()
	cancel()
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	if clone.Context().Value(redirectRequestKey{}) != nil {
		for name := range t.headers {
			clone.Header.Del(name)
		}
		response, err := t.base.RoundTrip(clone)
		return boundInboundResponse(response, err)
	}
	for name, values := range t.headers {
		clone.Header.Del(name)
		for _, value := range values {
			clone.Header.Add(name, value)
		}
	}
	response, err := t.base.RoundTrip(clone)
	return boundInboundResponse(response, err)
}

func boundInboundResponse(response *http.Response, err error) (*http.Response, error) {
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		response.Body = newBoundedSSEReader(response.Body)
		return response, nil
	}
	response.Body = newBoundedInboundReader(response.Body)
	return response, nil
}

func sameOriginRedirect(endpoint *url.URL) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, _ []*http.Request) error {
		if request.URL.Scheme != endpoint.Scheme || request.URL.Host != endpoint.Host {
			return http.ErrUseLastResponse
		}
		*request = *request.WithContext(context.WithValue(request.Context(), redirectRequestKey{}, true))
		return nil
	}
}

func connectStdio(ctx context.Context, server config.MCPServerConfig) (remoteClient, error) {
	// The startup context bounds the protocol handshake. Do not bind it to the
	// child process lifetime: EnsureServers cancels it after Connect returns.
	command := exec.Command(server.Command, server.Args...)
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
	session, err := sdk.NewClient(&sdk.Implementation{Name: "mivia", Version: "1"}, nil).Connect(ctx, &sdk.IOTransport{Reader: newBoundedStdioReader(out), Writer: in}, nil)
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
	Connect         func(context.Context, config.MCPServerConfig) (remoteClient, error)
	RedactionPolicy *redact.Policy
}

// Manager owns one lazy client per configured MCP server.
type Manager struct {
	cfg            config.MCPConfig
	connect        func(context.Context, config.MCPServerConfig) (remoteClient, error)
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	mu             sync.Mutex
	clients        map[string]remoteClient
	tools          map[string][]tools.Tool
	failures       map[string]error
	maxResultBytes int
	redaction      *redact.Policy
	closed         bool
}

// NewManager constructs a disconnected MCP manager.
func NewManager(cfg config.MCPConfig, opts ManagerOptions) (*Manager, error) {
	if !cfg.Enabled {
		shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
		return &Manager{cfg: cfg, shutdownCtx: shutdownCtx, shutdownCancel: shutdownCancel, clients: map[string]remoteClient{}, tools: map[string][]tools.Tool{}, failures: map[string]error{}, redaction: opts.RedactionPolicy}, nil
	}
	if opts.Connect == nil {
		opts.Connect = connectServer
	}
	if cfg.MaxServers > 0 && len(cfg.Servers) > cfg.MaxServers {
		return nil, fmt.Errorf("MCP server count exceeds configured maximum")
	}
	seen := make(map[string]struct{}, len(cfg.Servers))
	for _, server := range cfg.Servers {
		if _, ok := seen[server.ID]; ok {
			return nil, fmt.Errorf("duplicate MCP server %q", server.ID)
		}
		seen[server.ID] = struct{}{}
		if err := ValidateServerConfig(server); err != nil {
			return nil, err
		}
	}
	limit := cfg.MaxToolResultBytes
	if limit == 0 {
		limit = 64 << 10
	}
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	return &Manager{cfg: cfg, connect: opts.Connect, shutdownCtx: shutdownCtx, shutdownCancel: shutdownCancel, clients: map[string]remoteClient{}, tools: map[string][]tools.Tool{}, failures: map[string]error{}, maxResultBytes: limit, redaction: opts.RedactionPolicy}, nil
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
		if _, failed := m.failures[id]; failed {
			return nil, fmt.Errorf("MCP server %q is unavailable", id)
		}
		if _, ok := m.clients[id]; !ok {
			startupCtx, cancel := m.startupContext(ctx)
			client, err := m.connect(startupCtx, server)
			cancel()
			if err != nil {
				m.failures[id] = err
				return nil, fmt.Errorf("MCP server %q is unavailable", id)
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
				return nil, fmt.Errorf("MCP server %q is unavailable", id)
			}
			if m.cfg.MaxToolsPerServer > 0 && len(remote) > m.cfg.MaxToolsPerServer {
				_ = client.Close()
				delete(m.clients, id)
				m.failures[id] = fmt.Errorf("too many tools")
				return nil, fmt.Errorf("MCP server %q returned too many tools", id)
			}
			wrapped, err := wrapRemoteTools(id, client, remote, m.cfg.MaxToolDescriptionBytes, m.cfg.MaxToolSchemaBytes, m.maxResultBytes, server.TimeoutSeconds, m.redaction)
			if err != nil {
				_ = client.Close()
				delete(m.clients, id)
				m.failures[id] = err
				return nil, err
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

// OwnsTool reports whether name is one wrapper this manager discovered.
func (m *Manager) OwnsTool(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, wrappers := range m.tools {
		for _, wrapper := range wrappers {
			if wrapper.Name() == name {
				return true
			}
		}
	}
	return false
}

// Close closes every connected MCP client.
func (m *Manager) Close() error {
	m.shutdownCancel()
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
