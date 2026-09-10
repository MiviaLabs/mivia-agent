package cliautomations

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestServeCommandRejectsUnexpectedArgs(t *testing.T) {
	root := t.TempDir()
	if err := runServeCommand([]string{"--workspace", root, "extra"}); err == nil {
		t.Fatal("runServeCommand with an unexpected positional arg: got nil error, want rejection")
	}
}
