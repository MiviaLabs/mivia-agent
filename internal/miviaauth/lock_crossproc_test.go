package miviaauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// This file proves the refresh lock holds across a real OS process boundary.
//
// An in-process version of this test would pass against a plain sync.Mutex,
// so it could not tell the design this package chose from the one it
// rejected. The hazard is specifically two mivia PROCESSES sharing
// ~/.mivia/auth.json: flock is advisory and per open-file-description, and
// only a genuine second process exercises it the way two shells do.
//
// The child is this same test binary, re-entered through TestMain and
// selected by an environment variable, with marker files for
// synchronization. That is the pattern internal/storage/crossproc_test.go
// already establishes for the same reason; channels cannot cross a process
// boundary.

const (
	crossprocChildEnv   = "MIVIA_AUTH_CROSSPROC_CHILD"
	crossprocAuthEnv    = "MIVIA_AUTH_CROSSPROC_AUTH_PATH"
	crossprocServerEnv  = "MIVIA_AUTH_CROSSPROC_SERVER"
	crossprocReadyEnv   = "MIVIA_AUTH_CROSSPROC_READY"
	crossprocMarkerWait = 10 * time.Second
	crossprocPoll       = 5 * time.Millisecond
)

func TestMain(m *testing.M) {
	if os.Getenv(crossprocChildEnv) == "1" {
		os.Exit(runCrossprocChild())
	}
	os.Exit(m.Run())
}

// runCrossprocChild is the second process. It signals readiness, then calls
// Ensure against the same auth file and the same server as the parent.
func runCrossprocChild() int {
	authPath := os.Getenv(crossprocAuthEnv)
	client, err := NewClient(os.Getenv(crossprocServerEnv))
	if err != nil {
		return 2
	}
	if err := os.WriteFile(os.Getenv(crossprocReadyEnv), []byte("ready"), 0o600); err != nil {
		return 3
	}
	if _, err := NewService(client, authPath).Ensure(context.Background()); err != nil {
		// ErrRefreshBusy is a legitimate outcome for the loser: it means the
		// lock did its job and this process declined to race. What must never
		// happen is a second refresh reaching the server, which the parent
		// asserts.
		return 0
	}
	return 0
}

// TestEnsureCrossProcessRefreshesExactlyOnce starts a second real process
// against one session file, holds the server's refresh handler open until
// both are in flight, and asserts the server saw exactly one refresh.
//
// One refresh is the whole point: the token is one-time use, so a second
// request carrying the same value is the server's theft signal and costs BOTH
// processes the session.
func TestEnsureCrossProcessRefreshesExactlyOnce(t *testing.T) {
	// No environment guard, deliberately. If this binary cannot re-exec
	// itself, startCrossprocChild fails loudly with the real error -- a skip
	// here would silently retire the only test that proves the lock works
	// between processes, which is the entire reason it exists.
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	readyPath := filepath.Join(dir, "child-ready")

	srv, refreshes, release := startCountingRefreshServer(t)

	// A token inside the proactive window, so both processes want a refresh.
	mustSave(t, authPath, tokenAt("stale-bearer", "rt-shared", 1*time.Minute))

	child := startCrossprocChild(t, authPath, srv.URL, readyPath)
	waitForFile(t, readyPath)

	client, err := NewClient(srv.URL)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	parentDone := make(chan error, 1)
	go func() {
		_, err := NewService(client, authPath).Ensure(context.Background())
		parentDone <- err
	}()

	// Let the held refresh complete once both sides are certainly in play.
	time.Sleep(300 * time.Millisecond)
	release()

	if err := <-parentDone; err != nil {
		t.Fatalf("parent Ensure() error = %v", err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("child process failed: %v", err)
	}

	if got := refreshes.Load(); got != 1 {
		t.Fatalf("the server saw %d refreshes of a one-time-use token, want exactly 1: "+
			"a second one is the server's theft signal and revokes the session for both processes", got)
	}
	if stored := mustLoad(t, authPath); stored.RefreshToken != "rt-rotated" {
		t.Errorf("stored RefreshToken = %q, want the rotated value both processes must converge on", stored.RefreshToken)
	}
}

// startCountingRefreshServer serves /v1/auth/refresh, counts the calls, and
// holds the FIRST one open until the returned release is invoked -- which is
// what guarantees the second process is contending for the lock rather than
// arriving after the winner already finished.
func startCountingRefreshServer(t *testing.T) (*httptest.Server, *atomic.Int32, func()) {
	t.Helper()
	var (
		refreshes   atomic.Int32
		releaseOnce atomic.Bool
	)
	gate := make(chan struct{})
	release := func() {
		if releaseOnce.CompareAndSwap(false, true) {
			close(gate)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/refresh" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if refreshes.Add(1) == 1 {
			<-gate
		}
		body, _ := json.Marshal(map[string]any{
			"ok":           true,
			"token":        "rotated-bearer",
			"refreshToken": "rt-rotated",
			"user": map[string]string{
				"id": "u1", "email": "user@example.com",
				"organizationId": "org-1", "role": "member",
			},
			"expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(func() {
		release()
		srv.Close()
	})
	return srv, &refreshes, release
}

// startCrossprocChild re-execs this test binary as a genuine second process,
// pointed at the same session file and server.
func startCrossprocChild(t *testing.T, authPath, serverURL, readyPath string) *exec.Cmd {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run", "TestEnsureCrossProcessRefreshesExactlyOnce")
	child.Env = append(os.Environ(),
		crossprocChildEnv+"=1",
		crossprocAuthEnv+"="+authPath,
		crossprocServerEnv+"="+serverURL,
		crossprocReadyEnv+"="+readyPath,
	)
	if err := child.Start(); err != nil {
		t.Fatalf("start the second process: %v", err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	return child
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(crossprocMarkerWait)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(crossprocPoll)
	}
	t.Fatalf("timed out waiting for %s", path)
}
