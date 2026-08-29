package tools

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// TestNewBoundedHTTPClientDefaults verifies the package-default timeouts
// apply when a caller passes the zero-value config, so a tool that forgets
// to set anything still gets protection rather than an unbounded client.
func TestNewBoundedHTTPClientDefaults(t *testing.T) {
	client := newBoundedHTTPClient(boundedHTTPClientConfig{})
	if client.Timeout != toolHTTPOverallTimeout {
		t.Fatalf("Client.Timeout = %v, want default %v", client.Timeout, toolHTTPOverallTimeout)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.ResponseHeaderTimeout != toolHTTPResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v, want default %v", transport.ResponseHeaderTimeout, toolHTTPResponseHeaderTimeout)
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext must default to a bounded dialer, got nil")
	}
}

// TestNewBoundedHTTPClientOverridesTimeouts verifies explicit config values
// win over the package defaults, which the fetch_url slow-loris test relies
// on to run with fast, test-sized durations.
func TestNewBoundedHTTPClientOverridesTimeouts(t *testing.T) {
	client := newBoundedHTTPClient(boundedHTTPClientConfig{
		responseHeaderTimeout: 111 * time.Millisecond,
		overallTimeout:        222 * time.Millisecond,
	})
	if client.Timeout != 222*time.Millisecond {
		t.Fatalf("Client.Timeout = %v, want 222ms", client.Timeout)
	}
	transport := client.Transport.(*http.Transport)
	if transport.ResponseHeaderTimeout != 111*time.Millisecond {
		t.Fatalf("ResponseHeaderTimeout = %v, want 111ms", transport.ResponseHeaderTimeout)
	}
}

// TestNewBoundedHTTPClientProtectsAgainstSlowloris is the one place the
// slow-loris mechanism is proven: every tool built through
// newBoundedHTTPClient inherits this protection instead of each one needing
// its own copy of the same test. context.Background() carries no deadline,
// so only the client's own timeout can end the call.
func TestNewBoundedHTTPClientProtectsAgainstSlowloris(t *testing.T) {
	url := slowlorisServer(t)
	client := newBoundedHTTPClient(boundedHTTPClientConfig{
		responseHeaderTimeout: 200 * time.Millisecond,
		overallTimeout:        2 * time.Second,
	})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error against a server that never sends a response")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("newBoundedHTTPClient hung indefinitely with no ctx deadline")
	}
}

// TestWebSearchToolDeclaresCapabilityTimeout ensures search shares fetch_url's
// dispatcher-visible timeout declaration instead of falling back to the
// generic undeclared-tool default.
func TestWebSearchToolDeclaresCapabilityTimeout(t *testing.T) {
	tool := &webSearchTool{}
	capability := tool.Capability(nil)
	if capability.Timeout <= 0 {
		t.Fatalf("search must declare a positive Capability.Timeout, got %v", capability.Timeout)
	}
	if capability.Class != ExecutionExternal {
		t.Fatalf("search must declare ExecutionExternal, got %v", capability.Class)
	}
}

// TestExtractToolDeclaresCapabilityTimeout ensures extract, like search and
// fetch_url, is not invisible to dispatcher timeout policy.
func TestExtractToolDeclaresCapabilityTimeout(t *testing.T) {
	tool := &extractTool{}
	capability := tool.Capability(nil)
	if capability.Timeout <= 0 {
		t.Fatalf("extract must declare a positive Capability.Timeout, got %v", capability.Timeout)
	}
	if capability.Class != ExecutionExternal {
		t.Fatalf("extract must declare ExecutionExternal, got %v", capability.Class)
	}
}

// TestDefaultRegistryWiresBoundedWebClients proves the actual production
// wiring, not just the constructor in isolation: every network-backed tool
// NewDefaultRegistry builds must carry a client with the shared timeouts,
// not a raw &http.Client{} with no bound of its own.
func TestDefaultRegistryWiresBoundedWebClients(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws, TavilyAPIKey: "test-key"})

	searchRaw, ok := reg.Get("search")
	if !ok {
		t.Fatal("search not registered")
	}
	search, ok := searchRaw.(*webSearchTool)
	if !ok {
		t.Fatalf("search: got %T, want *webSearchTool", searchRaw)
	}
	if search.httpClient == nil || search.httpClient.Timeout <= 0 {
		t.Fatalf("search.httpClient must have a bounded Timeout, got %+v", search.httpClient)
	}

	fetchRaw, ok := reg.Get("fetch_url")
	if !ok {
		t.Fatal("fetch_url not registered")
	}
	fetch, ok := fetchRaw.(*fetchURLTool)
	if !ok {
		t.Fatalf("fetch_url: got %T, want *fetchURLTool", fetchRaw)
	}
	if fetch.httpClient == nil || fetch.httpClient.Timeout <= 0 {
		t.Fatalf("fetch_url.httpClient must have a bounded Timeout, got %+v", fetch.httpClient)
	}
	if fetch.fetchClient == nil || fetch.fetchClient.Timeout <= 0 {
		t.Fatalf("fetch_url.fetchClient must have a bounded Timeout, got %+v", fetch.fetchClient)
	}

	extractRaw, ok := reg.Get("extract")
	if !ok {
		t.Fatal("extract not registered (TavilyAPIKey was set)")
	}
	extract, ok := extractRaw.(*extractTool)
	if !ok {
		t.Fatalf("extract: got %T, want *extractTool", extractRaw)
	}
	if extract.httpClient == nil || extract.httpClient.Timeout <= 0 {
		t.Fatalf("extract.httpClient must have a bounded Timeout, got %+v", extract.httpClient)
	}
}
