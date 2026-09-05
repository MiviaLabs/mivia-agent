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
		*tools.DefaultOptions, string, *config.Resolved, func() *events.Bus, bool,
		ledger.LedgerRepository,
	) {
	}
	t.Cleanup(func() { cliagents.WireWorkflowToolOptionsVar = prev })

	reg, closeFn, err := cliagents.BuildToolsForRoot(unusableRoot, t.TempDir(), false, &config.Resolved{})
	if err == nil {
		t.Fatal("expected workspace-open failure")
	}
	if reg != nil || closeFn == nil {
		t.Fatalf("failure must return nil registry with a usable closer")
	}
	closeFn() // must be safe on the failure path
}

func TestBuildToolsForRoot_MemoryFailureAndHappyPath(t *testing.T) {
	wsRoot := t.TempDir()
	memRoot := filepath.Join(wsRoot, ".mivia") // parent exists; store opens/creates fine
	happyReg, closeFn, err := cliagents.BuildToolsForRoot(wsRoot, memRoot, false, &config.Resolved{})
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
	_, _, merr := cliagents.BuildToolsForRoot(wsRoot, nulMemRoot, false, &config.Resolved{})
	if merr == nil {
		t.Fatal("expected memory wiring failure on an unusable path")
	}
}
