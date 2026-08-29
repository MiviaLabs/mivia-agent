package tools

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestFetchURLDeclaresCapabilityTimeout ensures fetch_url is not invisible to
// dispatcher-level timeout policy: it must declare its own Capability with a
// positive Timeout and the external execution class, instead of silently
// falling back to the generic undeclared-tool default the way it does today.
func TestFetchURLDeclaresCapabilityTimeout(t *testing.T) {
	tool := &fetchURLTool{}
	capability := tool.Capability(nil)
	if capability.Timeout <= 0 {
		t.Fatalf("fetch_url must declare a positive Capability.Timeout, got %v", capability.Timeout)
	}
	if capability.Class != ExecutionExternal {
		t.Fatalf("fetch_url must declare ExecutionExternal, got %v", capability.Class)
	}
}

// slowlorisServer accepts a TCP connection and never writes a single byte
// back, simulating a server that completes the handshake and then goes
// silent forever. The connection is held open until the test cleans up.
func slowlorisServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		_ = ln.Close()
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				<-done
				_ = c.Close()
			}(conn)
		}
	}()
	return "http://" + ln.Addr().String() + "/"
}

// TestFetchURLClientHasResponseHeaderTimeout proves the fetch client itself
// bounds a slow-loris server, independent of any ctx deadline. The caller
// passes context.Background() (no deadline at all) so the only thing that
// can end the call is a timeout built into the http.Client/Transport.
func TestFetchURLClientHasResponseHeaderTimeout(t *testing.T) {
	url := slowlorisServer(t)
	tool := &fetchURLTool{
		fetchClient:       newSafeFetchHTTPClientWithTimeouts(200*time.Millisecond, 2*time.Second),
		allowPrivateFetch: true,
	}

	done := make(chan error, 1)
	go func() {
		_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"`+url+`"}`))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected fetch_url to fail against a server that never sends a response")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fetch_url hung indefinitely with no ctx deadline: the client declares no timeout of its own")
	}
}
