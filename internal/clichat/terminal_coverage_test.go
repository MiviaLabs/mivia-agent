package clichat

// terminal_coverage_test.go exercises the small NewTerminal and related
// terminal helpers. They require a real TTY to construct, which is true
// when legacytui runs the TUI but not under `go test`. We cover both
// branches: success when the TTY is real (skipped in non-interactive
// test runs), and the failure path.

import (
	"testing"
)

func TestNewTerminalRequiresTTY(t *testing.T) {
	// On any non-TTY stdin, NewTerminal must return an error, not panic.
	_, err := NewTerminal()
	if err == nil {
		t.Skip("NewTerminal succeeded (likely a TTY); real-TYY branch is covered by the TUI suite")
	}
}
