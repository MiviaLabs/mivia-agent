package provider

// Final overlay (Round-7 auditor recommendation): pins the DOCUMENTED
// local-daemon profile - base_url = http://127.0.0.1:<port>/v1 from
// docs/product/config.md and .mivia/mivia.toml.example - end to end. The
// daemon answers the first request with a 307 redirect to a NON-loopback
// TEST-NET-3 address; the follow-up dial must still land on the pinned
// 127.0.0.1 loopback listener (TEST-NET-3 is unroutable, so a successful
// redirect proves the rewrite), and a direct dial through the client
// transport with the redirect's host:port must also come back to loopback.
// TEST-ONLY: additive, touches no production code.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestFinalOverlayRedirectDialIsPinnedToLoopback proves the redirect pin
// holds for the documented 127.0.0.1 local-daemon profile exactly as it does
// for the localhost profile (see TestAuditRedirectDialIsPinnedToLoopback).
func TestFinalOverlayRedirectDialIsPinnedToLoopback(t *testing.T) {
	var (
		mu         sync.Mutex
		redirected []string
	)
	targetLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1: %v", err)
	}
	defer targetLn.Close()
	targetPort := portOf(t, targetLn.Addr().String())
	targetSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		redirected = append(redirected, r.Host)
		mu.Unlock()
		writeChatJSON(w, "pong")
	})}
	go targetSrv.Serve(targetLn)
	defer targetSrv.Close()

	daemonLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1: %v", err)
	}
	defer daemonLn.Close()
	daemonPort := portOf(t, daemonLn.Addr().String())
	daemonSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("http://203.0.113.7:%s/chat/completions", targetPort), http.StatusTemporaryRedirect)
	})}
	go daemonSrv.Serve(daemonLn)
	defer daemonSrv.Close()

	comp, err := NewOllama(Options{BaseURL: fmt.Sprintf("http://127.0.0.1:%s/v1", daemonPort)})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	client.client.Timeout = 10 * time.Second

	// Either phase transport proves the pin; both carry the same dial.
	tr := anyTransport(client)
	if tr == nil || tr.DialContext == nil {
		t.Fatal("keyless ollama client has no pinned DialContext")
	}

	// Direct-dial proof: dialing the redirect target's non-loopback address
	// through the client transport must be rewritten to the pinned 127.0.0.1
	// loopback listener (TEST-NET-3 itself is unroutable), and the connection
	// must come from a loopback source address.
	redirectAddr := net.JoinHostPort("203.0.113.7", targetPort)
	conn, err := tr.DialContext(context.Background(), "tcp", redirectAddr)
	if err != nil {
		t.Fatalf("direct dial of %q through the client transport failed (pin bypassed?): %v", redirectAddr, err)
	}
	if la, ok := conn.LocalAddr().(*net.TCPAddr); ok && !la.IP.IsLoopback() {
		conn.Close()
		t.Fatalf("direct dial connected from %v, want a loopback source address (rewrite)", la)
	}
	conn.Close()

	out, err := client.Chat(context.Background(), Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("redirect follow failed (pin bypassed?): %v", err)
	}
	if out != "pong" {
		t.Fatalf("out = %q, want pong", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(redirected) != 1 {
		t.Fatalf("redirect target saw %d requests, want 1", len(redirected))
	}
	// The Host header keeps the redirect URL's host; the DIAL must still be
	// pinned to loopback.
	if redirected[0] != redirectAddr {
		t.Fatalf("redirect request Host = %q, want %q", redirected[0], redirectAddr)
	}
}
