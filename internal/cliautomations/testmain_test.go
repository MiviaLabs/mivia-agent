package cliautomations

// testmain_test.go isolates HOME before any test in this package runs.
// This package resolves user configuration (config.Load merges the
// USER-level config even behind an explicit ConfigPath) and opens
// checkpoint stores under a workspace/user .mivia directory, so an
// unisolated HOME would let ambient developer-machine configuration leak
// into test outcomes and could write into the real home directory. See
// internal/testenv and internal/testenv/home_isolation_gate_test.go,
// which enforces that every package resolving user configuration defines
// this TestMain.

import (
	"fmt"
	"os"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/testenv"
)

func TestMain(m *testing.M) {
	restoreHome, err := testenv.IsolateHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testenv: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	restoreHome()
	os.Exit(code)
}
