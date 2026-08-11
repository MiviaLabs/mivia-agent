package verifier

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
)

func TestSandboxEnabledDefaultsTrue(t *testing.T) {
	if !SandboxEnabled() {
		t.Fatal("sandbox should default to enabled when SetSandboxEnabled was never called")
	}
}

func TestSetSandboxEnabledToggles(t *testing.T) {
	original := SandboxEnabled()
	t.Cleanup(func() { SetSandboxEnabled(original) })

	SetSandboxEnabled(false)
	if SandboxEnabled() {
		t.Fatal("SetSandboxEnabled(false) should disable the sandbox")
	}
	SetSandboxEnabled(true)
	if !SandboxEnabled() {
		t.Fatal("SetSandboxEnabled(true) should re-enable the sandbox")
	}
}

// TestRunVerifierCommandSkipsBwrapWhenDisabled proves the harness toggle, not
// just the config parsing: with the sandbox disabled and bubblewrap stubbed
// to an unusable path (so the sandboxed path would fail immediately if it
// ran), runVerifierCommand must still succeed via the direct-exec path.
func TestRunVerifierCommandSkipsBwrapWhenDisabled(t *testing.T) {
	original := SandboxEnabled()
	t.Cleanup(func() { SetSandboxEnabled(original) })
	stubBubblewrapPath(t, filepath.Join(t.TempDir(), "missing-bwrap"))
	SetSandboxEnabled(false)

	baseline := &GoModuleBaseline{GoMod: []byte("module example.com/test\n")}
	if err := runVerifierCommand(context.Background(), t.TempDir(), baseline, secretpath.Policy{}, "go", "version"); err != nil {
		t.Fatalf("runVerifierCommand() with sandbox disabled error = %v", err)
	}
}

// TestRunVerifierCommandUsesSandboxWhenEnabled is the mirror: with the
// default (enabled) toggle and bubblewrap stubbed unusable, the command must
// fail as a host failure - proving the sandboxed path, not the direct one,
// is what ran.
func TestRunVerifierCommandUsesSandboxWhenEnabled(t *testing.T) {
	original := SandboxEnabled()
	t.Cleanup(func() { SetSandboxEnabled(original) })
	stubBubblewrapPath(t, filepath.Join(t.TempDir(), "missing-bwrap"))
	SetSandboxEnabled(true)

	baseline := &GoModuleBaseline{GoMod: []byte("module example.com/test\n")}
	err := runVerifierCommand(context.Background(), t.TempDir(), baseline, secretpath.Policy{}, "go", "version")
	if err == nil {
		t.Fatal("runVerifierCommand() with sandbox enabled and bwrap unavailable should fail")
	}
	failure, ok := err.(*commandFailure)
	if !ok || failure.class != "host" {
		t.Fatalf("expected a host commandFailure, got %v", err)
	}
}
