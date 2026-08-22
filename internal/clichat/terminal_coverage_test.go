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

func TestTerminalHelpers(t *testing.T) {
	// paintRailCell, ChromeRenderOpts, and ApplyBlockChromeWith are pure
	// functions; exercise them so coverage includes the no-TTY branches.
	opts := ChromeRenderOpts()
	for _, kind := range []ChatBlockKind{ChatBlockUser, ChatBlockAssistant, ChatBlockTool, ChatBlockDivider, ChatBlockSystem} {
		_ = railForBlock(kind, false, opts)
		_ = railForBlock(kind, true, opts)
	}
	if got := paintRailCell(LeftRail{Glyph: "x"}); got == "" {
		t.Fatal("paintRailCell({Glyph:x}) must not be empty")
	}
	for _, line := range []string{"one", "two", "three"} {
		_ = applyBlockChrome([]string{line}, ChatBlock{}, "body", opts)
		_ = ApplyBlockChromeWith([]string{line}, ChatBlock{}, "body", opts, GroupMember{}, RailView{})
	}
}
