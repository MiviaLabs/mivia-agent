package mcp

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestManagerDoesNotHoldLockAcrossDiscovery pins that EnsureServers runs
// connect and ListTools without holding the manager mutex: the read accessors
// and Close must answer while discovery is still blocked on the network.
func TestManagerDoesNotHoldLockAcrossDiscovery(t *testing.T) {
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
	failuresDone := make(chan struct{})
	go func() { _ = manager.Failures(); close(failuresDone) }()
	select {
	case <-failuresDone:
	case <-time.After(100 * time.Millisecond):
		t.Error("Failures() blocked while discovery was active")
	}
	ownsDone := make(chan struct{})
	go func() { _ = manager.OwnsTool("anything"); close(ownsDone) }()
	select {
	case <-ownsDone:
	case <-time.After(100 * time.Millisecond):
		t.Error("OwnsTool() blocked while discovery was active")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Close() did not return while discovery was active")
	}
	select {
	case <-client.canceled:
	case <-time.After(100 * time.Millisecond):
		t.Error("Close() did not cancel the active discovery context")
	}
	cancelDiscovery()
	select {
	case <-ensureDone:
	case <-time.After(time.Second):
		t.Fatal("EnsureServers() did not return after discovery was canceled")
	}
}

// TestManagerSingleFlightsConcurrentEnsureServers pins the once-per-session
// contract under concurrency: two concurrent calls for the same id yield
// exactly one connect, both callers get the tools, and the second caller
// waits on the first caller's claim instead of hanging or reconnecting.
func TestManagerSingleFlightsConcurrentEnsureServers(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var connects atomic.Int32
	manager, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: stdioFixtureCommand(t),
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		connects.Add(1)
		close(entered)
		<-release
		return toolListClient{tools: []remoteTool{{Name: "read"}}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type ensureResult struct {
		tools []tools.Tool
		err   error
	}
	firstDone := make(chan ensureResult, 1)
	go func() {
		got, err := manager.EnsureServers(ctx, []string{"repository"})
		firstDone <- ensureResult{tools: got, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("EnsureServers() did not start connect")
	}
	secondDone := make(chan ensureResult, 1)
	go func() {
		got, err := manager.EnsureServers(ctx, []string{"repository"})
		secondDone <- ensureResult{tools: got, err: err}
	}()
	select {
	case result := <-secondDone:
		t.Fatalf("second EnsureServers() returned early: %d tools, %v", len(result.tools), result.err)
	case <-time.After(50 * time.Millisecond):
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("connect count = %d, want 1 while the claim is in flight", got)
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case result := <-firstDone:
			firstDone = nil
			if result.err != nil || len(result.tools) != 1 {
				t.Fatalf("first EnsureServers() = %d tools, %v; want the discovered tool", len(result.tools), result.err)
			}
		case result := <-secondDone:
			secondDone = nil
			if result.err != nil || len(result.tools) != 1 {
				t.Fatalf("second EnsureServers() = %d tools, %v; want the discovered tool", len(result.tools), result.err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent EnsureServers() call did not return")
		}
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("connect count = %d, want exactly 1 across both callers", got)
	}
}

// TestManagerConcurrentWaitersSeeContainedFailure pins that a failing connect
// with a concurrent waiter stays contained: both callers get no tools, the
// failure is recorded, and neither call hangs.
func TestManagerConcurrentWaitersSeeContainedFailure(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var connects atomic.Int32
	manager, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "down", Transport: "stdio", Command: stdioFixtureCommand(t),
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		connects.Add(1)
		close(entered)
		<-release
		return nil, errors.New("dial failure")
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type ensureResult struct {
		tools []tools.Tool
		err   error
	}
	firstDone := make(chan ensureResult, 1)
	go func() {
		got, err := manager.EnsureServers(ctx, []string{"down"})
		firstDone <- ensureResult{tools: got, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("EnsureServers() did not start connect")
	}
	secondDone := make(chan ensureResult, 1)
	go func() {
		got, err := manager.EnsureServers(ctx, []string{"down"})
		secondDone <- ensureResult{tools: got, err: err}
	}()
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case result := <-firstDone:
			firstDone = nil
			if result.err != nil || len(result.tools) != 0 {
				t.Fatalf("first EnsureServers() = %d tools, %v; want the failure contained", len(result.tools), result.err)
			}
		case result := <-secondDone:
			secondDone = nil
			if result.err != nil || len(result.tools) != 0 {
				t.Fatalf("second EnsureServers() = %d tools, %v; want the failure contained", len(result.tools), result.err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent EnsureServers() call did not return")
		}
	}
	if got := connects.Load(); got != 1 {
		t.Fatalf("connect count = %d, want 1", got)
	}
	failures := manager.Failures()
	if _, ok := failures["down"]; !ok {
		t.Fatalf("Failures() = %v, want the failed server recorded", failures)
	}
}

// TestManagerCloseDuringInFlightConnectDoesNotLeakClient pins that a connect
// still in flight when Close runs is closed by the commit path rather than
// stored, the pending claim is drained, and a later EnsureServers fails
// closed with "MCP manager is closed".
func TestManagerCloseDuringInFlightConnectDoesNotLeakClient(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	client := &closeSignalingClient{closed: make(chan struct{})}
	manager, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: stdioFixtureCommand(t),
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		close(entered)
		<-release
		return client, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ensureDone := make(chan error, 1)
	go func() { _, err := manager.EnsureServers(ctx, []string{"repository"}); ensureDone <- err }()
	select {
	case <-entered:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("EnsureServers() did not start connect")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Close() did not return while connect was in flight")
	}
	close(release)
	select {
	case <-client.closed:
	case <-time.After(time.Second):
		t.Error("in-flight client was not closed after Close()")
	}
	select {
	case err := <-ensureDone:
		if err != nil {
			t.Fatalf("EnsureServers() error = %v, want nil after Close()", err)
		}
	case <-time.After(time.Second):
		t.Fatal("EnsureServers() did not return after Close()")
	}
	manager.mu.Lock()
	pending := len(manager.pending)
	manager.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending claims = %d, want 0 after the in-flight connect resolved", pending)
	}
	if _, err := manager.EnsureServers(context.Background(), []string{"repository"}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("EnsureServers() after Close() error = %v, want MCP manager is closed", err)
	}
}

// slowCloseClient is a remoteClient whose Close blocks until release is
// closed, so a test can pin whether the manager holds its mutex while a
// client Close is in progress.
type slowCloseClient struct {
	started chan struct{}
	release chan struct{}
}

func (c *slowCloseClient) ListTools(context.Context) ([]remoteTool, error) { return nil, nil }
func (c *slowCloseClient) CallTool(context.Context, string, map[string]any) (string, error) {
	return "", nil
}
func (c *slowCloseClient) Close() error {
	close(c.started)
	<-c.release
	return nil
}

// TestManagerCloseDoesNotHoldMutexDuringClientClose pins that Manager.Close
// holds the manager mutex only for map bookkeeping: while a stored client's
// Close blocks, the read accessors must still answer promptly. The pre-fix
// Close called client.Close() under m.mu, so Failures() and OwnsTool() stalled
// behind a hung client close for up to mcpShutdownTimeout each, stalling the
// whole gateway during teardown.
func TestManagerCloseDoesNotHoldMutexDuringClientClose(t *testing.T) {
	client := &slowCloseClient{started: make(chan struct{}), release: make(chan struct{})}
	manager, err := NewManager(config.MCPConfig{Enabled: true, Servers: []config.MCPServerConfig{{
		ID: "repository", Transport: "stdio", Command: stdioFixtureCommand(t),
	}}}, ManagerOptions{Connect: func(context.Context, config.MCPServerConfig) (remoteClient, error) {
		return client, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureServers(context.Background(), []string{"repository"}); err != nil {
		t.Fatalf("EnsureServers() error = %v", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- manager.Close() }()
	select {
	case <-client.started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Close() did not reach the client Close")
	}
	failuresDone := make(chan struct{})
	go func() { _ = manager.Failures(); close(failuresDone) }()
	select {
	case <-failuresDone:
	case <-time.After(100 * time.Millisecond):
		t.Error("Failures() blocked while a client Close was in progress")
	}
	ownsDone := make(chan struct{})
	go func() { _ = manager.OwnsTool("anything"); close(ownsDone) }()
	select {
	case <-ownsDone:
	case <-time.After(100 * time.Millisecond):
		t.Error("OwnsTool() blocked while a client Close was in progress")
	}
	close(client.release)
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not return after the client close was released")
	}
}
