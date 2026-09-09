package chatsync

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/miviaauth"
)

// TestResolveEndpointPrefersConfigThenEnvThenDefault pins the resolution
// order and that the source label follows the value. DefaultBaseURL is the
// same function seen through one field, so this also covers what OpenSession
// dials.
func TestResolveEndpointPrefersConfigThenEnvThenDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv("MIVIA_API_BASE_URL", "")

	e := ResolveEndpoint("")
	if e.URL != miviaauth.DefaultServerURL || e.Source != miviaauth.ServerURLSourceDefault {
		t.Fatalf("nothing set: %+v", e)
	}
	if got := e.Describe(); got != "https://api.mivia.app (default)" {
		t.Fatalf("Describe() = %q", got)
	}

	t.Setenv("MIVIA_API_BASE_URL", "http://127.0.0.1:3001")
	e = ResolveEndpoint("")
	if e.URL != "http://127.0.0.1:3001" || !strings.Contains(e.Source, "MIVIA_API_BASE_URL") {
		t.Fatalf("env set: %+v", e)
	}

	e = ResolveEndpoint("  https://staging.invalid  ")
	if e.URL != "https://staging.invalid" || e.Source != "[sync] api_url" {
		t.Fatalf("config set: %+v, want the trimmed config value to win over the env", e)
	}
	if DefaultBaseURL("  https://staging.invalid  ") != e.URL {
		t.Fatalf("DefaultBaseURL disagrees with ResolveEndpoint")
	}
}

// TestProbeEndpointReportsWhatItReached pins the three answers the probe can
// give: reachable with the status it saw, unreachable with the dial error,
// and bounded - a host that accepts and then hangs must not hang doctor.
func TestProbeEndpointReportsWhatItReached(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()
	ok, detail := ProbeEndpoint(context.Background(), srv.URL+"/")
	if !ok || !strings.Contains(detail, "418") {
		t.Fatalf("live server: ok=%v detail=%q, want reachable with the status code", ok, detail)
	}
	if path != "/health" {
		t.Fatalf("probed %q, want the version-neutral /health route", path)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String()
	_ = l.Close()
	ok, detail = ProbeEndpoint(context.Background(), dead)
	if ok || !strings.HasPrefix(detail, "unreachable") {
		t.Fatalf("closed port: ok=%v detail=%q", ok, detail)
	}

	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hang.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	ok, _ = ProbeEndpoint(ctx, hang.URL)
	if ok || time.Since(start) > 2*time.Second {
		t.Fatalf("hanging server: ok=%v after %v; the probe must be bounded by the caller's context", ok, time.Since(start))
	}

	// Doctor passes a background context, so the probe's OWN bound is the
	// only thing between a hung host and a hung doctor command.
	prev := probeTimeout
	probeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { probeTimeout = prev })
	start = time.Now()
	ok, detail = ProbeEndpoint(context.Background(), hang.URL)
	if ok || time.Since(start) > 2*time.Second {
		t.Fatalf("hanging server, no caller deadline: ok=%v detail=%q after %v; the probe must bound itself", ok, detail, time.Since(start))
	}
}
