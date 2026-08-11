package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagerEnsuresOnlyRequestedServerOnce(t *testing.T) {
	var connects atomic.Int32
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: stdioFixtureCommand(t),
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

func TestManagerEnsureServersSkipsUnavailableServer(t *testing.T) {
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{
		{ID: "down", Transport: "stdio", Command: stdioFixtureCommand(t)},
		{ID: "up", Transport: "stdio", Command: stdioFixtureCommand(t)},
	}}, ManagerOptions{Connect: func(_ context.Context, server config.MCPServerConfig) (remoteClient, error) {
		if server.ID == "down" {
			return nil, errors.New("connect refused")
		}
		return toolListClient{tools: []remoteTool{{Name: "read"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.EnsureServers(context.Background(), []string{"down", "up"})
	if err != nil {
		t.Fatalf("EnsureServers() error = %v, want a contained per-server failure", err)
	}
	if len(got) != 1 {
		t.Fatalf("EnsureServers() returned %d tools, want only the healthy server's 1", len(got))
	}
	failures := m.Failures()
	if _, ok := failures["down"]; !ok {
		t.Fatalf("Failures() = %v, want a recorded failure for server %q", failures, "down")
	}
	if _, ok := failures["up"]; ok {
		t.Fatalf("Failures() = %v, want no failure for healthy server %q", failures, "up")
	}
}

func TestManagerEnsureServersKeepsUnavailableServerRecorded(t *testing.T) {
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{
		{ID: "down", Transport: "stdio", Command: stdioFixtureCommand(t)},
	}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		return nil, errors.New("connect refused")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsureServers(context.Background(), []string{"down"}); err != nil {
		t.Fatalf("EnsureServers() error = %v, want the failure contained", err)
	}
	got, err := m.EnsureServers(context.Background(), []string{"down"})
	if err != nil {
		t.Fatalf("second EnsureServers() error = %v, want the server skipped, not fatal", err)
	}
	if len(got) != 0 {
		t.Fatalf("second EnsureServers() = %d tools, want 0 for the unavailable server", len(got))
	}
	if len(m.Failures()) != 1 {
		t.Fatalf("Failures() = %v, want exactly the one unavailable server", m.Failures())
	}
}

func TestManagerEnsureServersContainsDiscoveryFailure(t *testing.T) {
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{
		{ID: "down", Transport: "stdio", Command: stdioFixtureCommand(t)},
	}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		return discoveryFailClient{err: errors.New("discovery refused")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.EnsureServers(context.Background(), []string{"down"})
	if err != nil {
		t.Fatalf("EnsureServers() error = %v, want the discovery failure contained", err)
	}
	if len(got) != 0 {
		t.Fatalf("EnsureServers() = %d tools, want 0 for the failed server", len(got))
	}
	if len(m.Failures()) != 1 {
		t.Fatalf("Failures() = %v, want the failed server recorded", m.Failures())
	}
}

func TestManagerCloseCancelsBlockingDiscovery(t *testing.T) {
	client := &blockingDiscoveryClient{started: make(chan struct{}), canceled: make(chan struct{})}
	manager, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: stdioFixtureCommand(t),
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		return client, nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	discoveryCtx, cancelDiscovery := context.WithCancel(context.Background())
	defer cancelDiscovery()
	ensureDone := make(chan error, 1)
	go func() { _, err := manager.EnsureServers(discoveryCtx, []string{"repository"}); ensureDone <- err }()
	select {
	case <-client.started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("EnsureServers() did not start discovery")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	closeReturned := false
	select {
	case err := <-closeDone:
		closeReturned = true
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Close() did not return while discovery was active")
	}
	select {
	case <-client.canceled:
	case <-time.After(100 * time.Millisecond):
		t.Error("Close() did not cancel the active discovery context")
	}

	cancelDiscovery()
	<-ensureDone
	if !closeReturned {
		select {
		case err := <-closeDone:
			if err != nil {
				t.Errorf("Close() cleanup error = %v", err)
			}
		case <-time.After(time.Second):
			t.Error("Close() did not return during test cleanup")
		}
	}
}

func TestStreamableHTTPDiscoversAndCallsTool(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "returns text"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "reply"}}}, nil, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client, err := connectStreamableHTTP(context.Background(), config.MCPServerConfig{Transport: "streamable_http", URL: httpServer.URL})
	if err != nil {
		t.Fatalf("connectStreamableHTTP() error = %v", err)
	}
	defer client.Close()
	remote, err := client.ListTools(context.Background())
	if err != nil || len(remote) != 1 || remote[0].Name != "echo" {
		t.Fatalf("ListTools() = %#v, %v", remote, err)
	}
	result, err := client.CallTool(context.Background(), "echo", map[string]any{})
	if err != nil || result != "reply" {
		t.Fatalf("CallTool() = %q, %v", result, err)
	}
}

func TestStreamableHTTPRejectsErrorToolResult(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "fail"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: "untrusted server failure"}},
			IsError: true,
		}, nil, nil
	})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{JSONResponse: true}))
	defer httpServer.Close()

	client, err := connectStreamableHTTP(context.Background(), config.MCPServerConfig{Transport: "streamable_http", URL: httpServer.URL})
	if err != nil {
		t.Fatalf("connectStreamableHTTP() error = %v", err)
	}
	defer client.Close()
	result, err := client.CallTool(context.Background(), "fail", map[string]any{})
	if err == nil {
		t.Fatalf("CallTool() = %q, nil error; want tool-result error", result)
	}
	if result != "" {
		t.Fatalf("CallTool() result = %q, want no untrusted error text", result)
	}
}

func TestStdioDiscoversCallsAndReapsProcess(t *testing.T) {
	t.Setenv("MIVIA_MCP_HELPER", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := connectStdio(ctx, config.MCPServerConfig{
		Command: os.Args[0], Args: []string{"-test.run=^TestStdioMCPHelper$"}, Env: []string{"MIVIA_MCP_HELPER"},
	})
	if err != nil {
		t.Fatalf("connectStdio() error = %v", err)
	}
	remote, err := client.ListTools(ctx)
	if err != nil || len(remote) != 1 || remote[0].Name != "echo" {
		t.Fatalf("ListTools() = %#v, %v", remote, err)
	}
	result, err := client.CallTool(ctx, "echo", map[string]any{})
	if err != nil || result != "reply" {
		t.Fatalf("CallTool() = %q, %v", result, err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestManagerStdioDiscoverySurvivesStartupContext(t *testing.T) {
	t.Setenv("MIVIA_MCP_HELPER", "1")
	manager, err := NewManager(config.MCPConfig{Enabled: true, StartupTimeoutSeconds: 1, Servers: []config.MCPServerConfig{{
		ID: "stdio", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestStdioMCPHelper$"}, Env: []string{"MIVIA_MCP_HELPER"},
	}}}, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	wrappers, err := manager.EnsureServers(context.Background(), []string{"stdio"})
	if err != nil || len(wrappers) != 1 {
		t.Fatalf("EnsureServers() = %#v, %v", wrappers, err)
	}
}

func TestStdioMCPHelper(t *testing.T) {
	if os.Getenv("MIVIA_MCP_HELPER") != "1" {
		return
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "returns text"}, func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "reply"}}}, nil, nil
	})
	session, err := server.Connect(context.Background(), &sdk.IOTransport{Reader: os.Stdin, Writer: os.Stdout}, nil)
	if err != nil {
		os.Exit(2)
	}
	if err := session.Wait(); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
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

func TestSameOriginRedirectStripsConfiguredHeaders(t *testing.T) {
	endpoint, err := url.Parse("https://example.test/mcp")
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, "https://example.test/next", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sameOriginRedirect(endpoint)(request, nil); err != nil {
		t.Fatal(err)
	}
	var got *http.Request
	transport := headerTransport{
		headers: http.Header{"Authorization": []string{"secret"}},
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			got = request
			return nil, nil
		}),
	}
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Header.Get("Authorization") != "" {
		t.Fatal("redirect request retained configured HTTP headers")
	}
}

func TestHeaderTransportStripsHeadersFromMarkedRedirect(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "https://example.test/next", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "secret")
	request = request.WithContext(context.WithValue(request.Context(), redirectRequestKey{}, true))
	var got *http.Request
	transport := headerTransport{
		headers: http.Header{"Authorization": []string{"secret"}},
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			got = request
			return nil, nil
		}),
	}
	if _, err := transport.RoundTrip(request); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Header.Get("Authorization") != "" {
		t.Fatal("marked redirect retained configured HTTP headers")
	}
}

func TestManagerRejectsMoreToolsThanConfigured(t *testing.T) {
	m, err := NewManager(config.MCPConfig{Enabled: true, MaxToolsPerServer: 1, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: stdioFixtureCommand(t),
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		return toolListClient{tools: []remoteTool{{Name: "one"}, {Name: "two"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.EnsureServers(context.Background(), []string{"repository"})
	if err != nil {
		t.Fatalf("EnsureServers() error = %v, want the too-many-tools failure contained", err)
	}
	if len(got) != 0 {
		t.Fatalf("EnsureServers() = %d tools, want 0 for the too-many-tools server", len(got))
	}
	if len(m.Failures()) != 1 {
		t.Fatalf("Failures() = %v, want the too-many-tools server recorded", m.Failures())
	}
}

func TestManagerMemoizesFailedDiscovery(t *testing.T) {
	var connects atomic.Int32
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: stdioFixtureCommand(t),
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		connects.Add(1)
		return nil, errors.New("dial failure")
	}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := m.EnsureServers(context.Background(), []string{"repository"})
		if err != nil {
			t.Fatalf("EnsureServers() error = %v, want the per-server failure contained", err)
		}
		if len(got) != 0 {
			t.Fatalf("EnsureServers() = %d tools, want 0 for the failed server", len(got))
		}
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("connect count = %d, want 1 after failure", got)
	}
	if len(m.Failures()) != 1 {
		t.Fatalf("Failures() = %v, want the failed server recorded", m.Failures())
	}
}

func TestManagerAppliesServerTimeout(t *testing.T) {
	m, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: stdioFixtureCommand(t), TimeoutSeconds: 1,
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
		ID: "repository", Transport: "stdio", Command: stdioFixtureCommand(t),
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

func TestHTTPTransportBoundsInboundResponse(t *testing.T) {
	transport := headerTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxMCPInboundMessageBytes+1))),
		}, nil
	})}
	request, err := http.NewRequest(http.MethodPost, "https://example.test/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(response.Body)
	if readErr == nil || len(body) > maxMCPInboundMessageBytes {
		t.Fatalf("inbound HTTP response len=%d err=%v, want bounded error", len(body), readErr)
	}
}

func TestHTTPTransportAllowsInboundResponseAtLimit(t *testing.T) {
	transport := headerTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", maxMCPInboundMessageBytes))),
		}, nil
	})}
	request, err := http.NewRequest(http.MethodPost, "https://example.test/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read inbound HTTP response: %v", err)
	}
	if len(body) != maxMCPInboundMessageBytes {
		t.Fatalf("inbound HTTP response len=%d, want %d", len(body), maxMCPInboundMessageBytes)
	}
}

func TestSSEReaderBoundsEachEvent(t *testing.T) {
	event := strings.Repeat("x", maxMCPInboundMessageBytes-2) + "\n\n"
	response, err := boundInboundResponse(&http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader(event + event)),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read inbound SSE stream: %v", err)
	}
	if len(body) != len(event)*2 {
		t.Fatalf("inbound SSE stream len=%d, want %d", len(body), len(event)*2)
	}
}

func TestSSEReaderAllowsCRLFSeparatedEvents(t *testing.T) {
	event := strings.Repeat("x", maxMCPInboundMessageBytes-4) + "\r\n\r\n"
	response, err := boundInboundResponse(&http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader(event + event)),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read inbound CRLF SSE stream: %v", err)
	}
	if len(body) != len(event)*2 {
		t.Fatalf("inbound CRLF SSE stream len=%d, want %d", len(body), len(event)*2)
	}
}

func TestStdioReaderBoundsInboundMessage(t *testing.T) {
	reader := newBoundedStdioReader(io.NopCloser(strings.NewReader(strings.Repeat("x", maxMCPInboundMessageBytes+1) + "\n")))
	body, err := io.ReadAll(reader)
	if err == nil || len(body) > maxMCPInboundMessageBytes {
		t.Fatalf("inbound stdio message len=%d err=%v, want bounded error", len(body), err)
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

func TestSerializedRemoteClientCloseCancelsActiveCall(t *testing.T) {
	client := &closeProbeClient{started: make(chan struct{}), canceled: make(chan struct{})}
	wrapped := &serializedRemoteClient{client: client}
	go func() { _, _ = wrapped.CallTool(context.Background(), "tool", nil) }()
	<-client.started

	closed := make(chan error, 1)
	go func() { closed <- wrapped.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Close() did not return while CallTool() was active")
	}
	select {
	case <-client.canceled:
	case <-time.After(100 * time.Millisecond):
		t.Error("Close() did not cancel the active CallTool() context")
	}
}

func TestSerializedRemoteClientCloseDoesNotWaitForIgnoredCancellation(t *testing.T) {
	client := &blockingCloseClient{started: make(chan struct{}), release: make(chan struct{})}
	wrapped := &serializedRemoteClient{client: client}
	go func() { _, _ = wrapped.CallTool(context.Background(), "tool", nil) }()
	<-client.started

	closed := make(chan error, 1)
	go func() { closed <- wrapped.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Close() waited for an operation that ignored cancellation")
	}
	close(client.release)
}

func TestCapMCPResultNeverExceedsLimit(t *testing.T) {
	for _, limit := range []int{1, 10, 32} {
		got := capMCPResult("abcdefghijk", limit)
		if len(got) > limit {
			t.Fatalf("capMCPResult() length = %d, limit = %d", len(got), limit)
		}
	}
}

type fakeRemoteClient struct{}

type blockingDiscoveryClient struct {
	started  chan struct{}
	canceled chan struct{}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func (fakeRemoteClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (fakeRemoteClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", nil
}
func (fakeRemoteClient) Close() error { return nil }

func (c *blockingDiscoveryClient) ListTools(ctx context.Context) ([]remoteTool, error) {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	return nil, ctx.Err()
}
func (c *blockingDiscoveryClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", nil
}
func (c *blockingDiscoveryClient) Close() error { return nil }

type toolListClient struct{ tools []remoteTool }

func (c toolListClient) ListTools(context.Context) ([]remoteTool, error) { return c.tools, nil }
func (toolListClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", nil
}
func (toolListClient) Close() error { return nil }

type discoveryFailClient struct{ err error }

func (c discoveryFailClient) ListTools(context.Context) ([]remoteTool, error) { return nil, c.err }
func (discoveryFailClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", nil
}
func (discoveryFailClient) Close() error { return nil }

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

type closeProbeClient struct {
	started  chan struct{}
	canceled chan struct{}
}

func (c *closeProbeClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (c *closeProbeClient) CallTool(ctx context.Context, _ string, _ map[string]any) (string, error) {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	return "", ctx.Err()
}
func (c *closeProbeClient) Close() error { return nil }

type blockingCloseClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingCloseClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (c *blockingCloseClient) CallTool(context.Context, string, map[string]any) (string, error) {
	close(c.started)
	<-c.release
	return "", nil
}
func (c *blockingCloseClient) Close() error { return nil }

type closeSignalingClient struct {
	once   sync.Once
	closed chan struct{}
}

func (c *closeSignalingClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (c *closeSignalingClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", nil
}
func (c *closeSignalingClient) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
