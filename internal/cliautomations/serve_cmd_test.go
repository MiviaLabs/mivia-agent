package cliautomations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// TestServeCommandExitsCleanlyOnSignal proves `automations serve` stops
// and returns nil once its context is cancelled - substituting
// serveSignalContext with a plain cancellable context rather than
// sending the test process a real OS signal (unsafe under -race and in
// a shared test binary: an unhandled SIGTERM before the real
// signal.NotifyContext handler installs terminates the whole binary).
func TestServeCommandExitsCleanlyOnSignal(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := `[provider]
name = "openrouter"

[providers.openrouter]
default_model = "test/model"
base_url = "http://127.0.0.1:0"
api_key_env = "CLIAUTOMATIONS_SERVE_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]

[subagents]
store_path = ".mivia/context.db"
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	t.Setenv("CLIAUTOMATIONS_SERVE_TEST_KEY", "test-key")

	ctx, cancel := context.WithCancel(context.Background())
	prev := serveSignalContext
	serveSignalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { serveSignalContext = prev })

	errCh := make(chan error, 1)
	go func() {
		errCh <- runServeCommand([]string{"--workspace", root})
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runServeCommand after cancel = %v, want nil (clean shutdown)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServeCommand did not return within 5s of cancellation")
	}
}

// TestServeCommandPropagatesNonCancelServeError covers runServeCommand's
// own `return err` branch (serve_cmd.go's `if err != nil &&
// !errors.Is(err, context.Canceled) { return err }`): Service.Serve's
// loop only ever returns ctx.Err() (serve.go:230), and ctx.Err() is NOT
// always context.Canceled - a context.WithTimeout context that expires
// on its own (never explicitly Cancel()-ed) reports
// context.DeadlineExceeded instead, which fails the errors.Is check and
// must propagate as a real error, not be swallowed as a clean shutdown.
func TestServeCommandPropagatesNonCancelServeError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mivia"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := `[provider]
name = "openrouter"

[providers.openrouter]
default_model = "test/model"
base_url = "http://127.0.0.1:0"
api_key_env = "CLIAUTOMATIONS_SERVE_DEADLINE_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]

[subagents]
store_path = ".mivia/context.db"
`
	if err := os.WriteFile(filepath.Join(root, ".mivia", "mivia.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write mivia.toml: %v", err)
	}
	t.Setenv("CLIAUTOMATIONS_SERVE_DEADLINE_TEST_KEY", "test-key")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	prev := serveSignalContext
	serveSignalContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	t.Cleanup(func() { serveSignalContext = prev })

	err := runServeCommand([]string{"--workspace", root})
	if err == nil {
		t.Fatal("runServeCommand after a deadline expiry: got nil error, want context.DeadlineExceeded propagated")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runServeCommand after a deadline expiry = %v, want context.DeadlineExceeded", err)
	}
}

// TestServeCommandSweepsInterruptedAtStartup proves runServeCommand's
// D13 startup sweep (serve_cmd.go's svc.SweepInterrupted(ctx) call,
// placed before svc.Serve(ctx)) actually executes: seed a RunRunning
// row with NO fenced claim at all for its automation (claim.go's
// sweepInterrupted treats a running row with no claim the same as an
// expired one - see runstore_test.go's own
// TestSweepInterruptedFlagsRunningWithNoClaim - so this is reachable
// regardless of any staleness threshold, the cheapest fixture
// available), start runServeCommand under a context this test
// controls, let it run briefly (long enough for the synchronous
// startup sweep to complete before Serve's own tick loop begins),
// cancel to get a clean shutdown, then read the run back via a FRESH
// *storage.SQLite handle (proving the sweep's write is durable and
// visible after runServeCommand's own cleanup() has closed its
// handle) and assert its state moved to "interrupted".
func TestServeCommandSweepsInterruptedAtStartup(t *testing.T) {
	root := writeAutomationsFixture(t, "sweep-startup-auto")
	insertAutomationRun(t, root, storage.AutomationRun{
		ID:           "sweep-startup-run",
		AutomationID: "sweep-startup-auto",
		Origin:       "scheduled",
		State:        "running",
		StartedAt:    time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	})

	ctx, cancel := context.WithCancel(context.Background())
	prev := serveSignalContext
	serveSignalContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { serveSignalContext = prev })

	errCh := make(chan error, 1)
	go func() { errCh <- runServeCommand([]string{"--workspace", root}) }()

	time.Sleep(200 * time.Millisecond) // let the synchronous startup sweep run before we cancel
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runServeCommand after cancel = %v, want nil (clean shutdown)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServeCommand did not return within 5s of cancellation")
	}

	db, err := storage.OpenSQLite(automationStorePath(t, root))
	if err != nil {
		t.Fatalf("open automation store: %v", err)
	}
	defer db.Close()
	row, ok, err := db.GetAutomationRun(context.Background(), "sweep-startup-run")
	if err != nil || !ok {
		t.Fatalf("GetAutomationRun: ok=%v err=%v", ok, err)
	}
	if row.State != "interrupted" {
		t.Fatalf("run state after serve's startup sweep = %q, want %q", row.State, "interrupted")
	}
}

func TestServeCommandRejectsUnexpectedArgs(t *testing.T) {
	root := t.TempDir()
	if err := runServeCommand([]string{"--workspace", root, "extra"}); err == nil {
		t.Fatal("runServeCommand with an unexpected positional arg: got nil error, want rejection")
	}
}
