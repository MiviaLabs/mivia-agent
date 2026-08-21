package cliworktree_test

// External (black-box) test package: needed only so this test can import
// internal/cli (for cli.Execute) without the Go toolchain rejecting it as an
// import cycle. cli imports internal/cliworktree, so an in-package test file
// in cliworktree cannot import cli - see worktree_command_test.go's doc
// comment above where this test used to live. This import also runs cli's
// init (cliworktree_wiring.go), which wires
// cliworktree.OpenRepositoryContextStoreFunc for every test in this binary,
// internal and external alike.

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
)

func TestExecuteWorktreeCommandIsRegistered(t *testing.T) {
	err := cli.Execute([]string{"worktree"})
	if err == nil || !strings.Contains(err.Error(), "expected create, list, remove, or adopt") {
		t.Fatalf("error = %v, want worktree usage error", err)
	}
}
