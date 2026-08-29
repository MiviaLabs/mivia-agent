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

// mcpWaitDelay bounds the wait for pipes an MCP server's own grandchildren
// still hold after the server is killed.
const mcpWaitDelay = 5 * time.Second

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
	// Bounds the Kill-then-Wait shutdown below: a stdio server that spawns its
	// own helpers leaves them holding these pipes, and Wait does not return
	// while they do. Killing the server alone is not enough.
	command.WaitDelay = mcpWaitDelay
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
	pending        map[string]chan struct{}
	maxResultBytes int
	redaction      *redact.Policy
	closed         bool
}

// NewManager constructs a disconnected MCP manager.
func NewManager(cfg config.MCPConfig, opts ManagerOptions) (*Manager, error) {
	if !cfg.Enabled {
		shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
		return &Manager{cfg: cfg, shutdownCtx: shutdownCtx, shutdownCancel: shutdownCancel, clients: map[string]remoteClient{}, tools: map[string][]tools.Tool{}, failures: map[string]error{}, pending: map[string]chan struct{}{}, redaction: opts.RedactionPolicy}, nil
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
	// The cancel function is owned by the manager: every success path stores it
	// and Close cancels it, and no error path creates it (a leaked cancel would
	// keep a background context alive forever).
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	return &Manager{cfg: cfg, connect: opts.Connect, shutdownCtx: shutdownCtx, shutdownCancel: shutdownCancel, clients: map[string]remoteClient{}, tools: map[string][]tools.Tool{}, failures: map[string]error{}, pending: map[string]chan struct{}{}, maxResultBytes: limit, redaction: opts.RedactionPolicy}, nil
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

// Failures reports the servers whose connection or discovery failed, keyed by
// server ID. A contained server outage never fails a session or a workflow
// start; callers use this accessor to surface which tools are absent and why.
func (m *Manager) Failures() map[string]error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]error, len(m.failures))
	for id, err := range m.failures {
		out[id] = err
	}
	return out
}

// Close closes every connected MCP client. The manager mutex is held only for
// map bookkeeping: the closed flag is set and the clients are snapshotted
// under the lock, then each client's Close runs outside it, so a hung client
// close (bounded by mcpShutdownTimeout) can never stall the accessors,
// EnsureServers, or a concurrent Close. commitClaim's closed check still
// closes an in-flight connect's client instead of storing it, so no client
// leaks or double-closes (serializedRemoteClient.Close is idempotent).
func (m *Manager) Close() error {
	m.shutdownCancel()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	clients := make([]remoteClient, 0, len(m.clients))
	for _, client := range m.clients {
		clients = append(clients, client)
	}
	m.mu.Unlock()
	var first error
	for _, client := range clients {
		if err := client.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
