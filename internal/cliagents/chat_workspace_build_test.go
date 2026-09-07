package cliagents_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// The NUL byte makes every path syscall (EvalSymlinks/Open) fail without
// depending on platform-specific reserved names.
var unusableRoot = string([]byte{0})

func TestBuildToolsForRoot_WorkspaceFailurePropagates(t *testing.T) {
	prev := cliagents.WireWorkflowToolOptionsVar
	cliagents.WireWorkflowToolOptionsVar = func(
		*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool, bool,
		ledger.LedgerRepository,
	) {
	}
	t.Cleanup(func() { cliagents.WireWorkflowToolOptionsVar = prev })

	reg, closeFn, err := cliagents.BuildToolsForRoot(unusableRoot, t.TempDir(), false, &config.Resolved{}, cliagents.SessionRootWiring{})
	if err == nil {
		t.Fatal("expected workspace-open failure")
	}
	if reg != nil || closeFn == nil {
		t.Fatalf("failure must return nil registry with a usable closer")
	}
	closeFn() // must be safe on the failure path
}

// The memory index is a derived cache, so an unusable memory path must not
// stop tool-registry construction (and with it the CLI): the store opens in a
// degraded state and the underlying failure surfaces when a memory tool is
// actually used.
func TestBuildToolsForRoot_MemoryDegradesAndHappyPath(t *testing.T) {
	wsRoot := t.TempDir()
	memRoot := filepath.Join(wsRoot, ".mivia") // parent exists; store opens/creates fine
	happyReg, closeFn, err := cliagents.BuildToolsForRoot(wsRoot, memRoot, false, &config.Resolved{}, cliagents.SessionRootWiring{})
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if happyReg == nil {
		t.Fatal("nil registry on happy path")
	}
	closeFn()

	nulMemRoot := strings.Map(func(r rune) rune {
		if r == 'a' {
			return 0
		}
		return r
	}, filepath.Join(wsRoot, ".mivia"))
	// filepath.Abs is pure string manipulation on POSIX but is backed by
	// syscall.FullPath on Windows, which rejects the NUL outright. Where the
	// path never reaches the store, there is no degrade to observe.
	if _, probeErr := filepath.Abs(nulMemRoot); probeErr != nil {
		t.Skipf("platform rejects a NUL path before the store can degrade: %v", probeErr)
	}
	degradedReg, degradedClose, derr := cliagents.BuildToolsForRoot(wsRoot, nulMemRoot, false, &config.Resolved{}, cliagents.SessionRootWiring{})
	if derr != nil {
		t.Fatalf("unusable memory path must degrade, not fail wiring: %v", derr)
	}
	if degradedReg == nil {
		t.Fatal("nil registry on degraded memory path")
	}
	degradedClose()
}
