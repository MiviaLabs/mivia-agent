package uiadapter

import (
	"context"
	"testing"
)

// TestCompleteLogin_NilLoginServiceFallsBackToDefault pins the
// r.loginService == nil fallback: a bare CommandRunner (never routed
// through NewCommandRunner/NewCommandRunnerWithPool, which always set the
// seam) must still resolve a usable Service via miviaauth.DefaultService
// rather than panicking on a nil svcFn call.
func TestCompleteLogin_NilLoginServiceFallsBackToDefault(t *testing.T) {
	// Point the fallback's own DefaultService at an unreachable local
	// loopback port rather than the real API - this test asserts the seam
	// falls back and runs, not that it succeeds, and must never make a
	// real outbound network call.
	t.Setenv("MIVIA_API_BASE_URL", "http://127.0.0.1:1")
	r := &CommandRunner{}
	out := r.CompleteLogin(context.Background(), "user@example.com", []byte("pw"))
	if out.LoginPrompt {
		t.Fatal("CompleteLogin re-opened the login prompt; want it to have attempted the fallback service")
	}
}
