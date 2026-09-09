package uiadapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestLaunchCheckoutDir_FallsBackToAgentStateWhenNoPool pins
// launchCheckoutDir's own r.agentState.WorkspaceRoot fallback: a runner
// with no pool (a hand-built runner, per the function's own doc comment)
// but a wired agentState must resolve from that state's WorkspaceRoot
// rather than falling all the way through to os.Getwd.
func TestLaunchCheckoutDir_FallsBackToAgentStateWhenNoPool(t *testing.T) {
	dir := t.TempDir()
	// Built directly rather than via NewCommandRunner, which always
	// constructs a live pool - this is the "hand-built runner" case the
	// function's own doc comment describes, with r.pool genuinely nil.
	r := &CommandRunner{agentState: &cliagents.AgentSessionState{WorkspaceRoot: dir}}

	got, err := r.launchCheckoutDir()
	if err != nil {
		t.Fatalf("launchCheckoutDir: %v", err)
	}
	wantAbs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantAbs {
		t.Fatalf("launchCheckoutDir() = %q, want %q", got, wantAbs)
	}
}

// TestLaunchCheckoutDir_AbsErrorSurfaces pins launchCheckoutDir's own
// filepath.Abs error wrap on the pool branch: a relative WorkspaceRoot
// makes filepath.Abs call os.Getwd internally, which fails once the
// process's working directory has been removed out from under it - the
// only deterministic, in-process way to make Abs itself fail.
func TestLaunchCheckoutDir_AbsErrorSurfaces(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gone := t.TempDir()
	if err := os.Chdir(gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.RemoveAll(gone); err != nil {
		t.Skipf("platform will not remove its own working directory: %v", err)
	}
	if _, probeErr := filepath.Abs("relative-root"); probeErr == nil {
		t.Skip("platform still resolves an absolute path from a removed working directory")
	}

	pool := NewSessionPool(nil, &config.Resolved{}, &cliagents.AgentSessionState{WorkspaceRoot: "relative-root"}, false)
	r := NewCommandRunnerWithPool(nil, pool, &config.Resolved{}, nil)

	if _, err := r.launchCheckoutDir(); err == nil {
		t.Fatal("launchCheckoutDir accepted an unresolvable relative WorkspaceRoot")
	}
}
