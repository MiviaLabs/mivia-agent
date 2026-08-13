package controller

import "testing"

func TestPanelWorkLimitSlicesAreFixed(t *testing.T) {
	// MaxTurns must stay 0 (unlimited) in the base slices: the turn bound is a
	// per-step workflow knob (definition.Step.MaxTurns, default 0 = unlimited)
	// applied at build time in buildPanelAttempt/buildPanelSynthesisWork. A
	// hardcoded cap (historically 16 for members, 8 for synthesis) killed deep
	// read-only reviews mid-panel with "agent exceeded max_steps (16)".
	if got, want := panelMemberLimits.MaxTurns, 0; got != want {
		t.Fatalf("member max turns = %d, want %d (unlimited)", got, want)
	}
	// MaxPromptTokens must stay 0 (unlimited cumulative): a finite cap killed
	// deep read-only reviews mid-panel with "work limit exceeded: prompt tokens"
	// (see panel_attempt.go). Per-call context is window-bounded with compaction.
	if got, want := panelMemberLimits.MaxPromptTokens, 0; got != want {
		t.Fatalf("member prompt limit = %d, want %d (unlimited)", got, want)
	}
	if got, want := panelMemberLimits.MaxOutputTokens, 131072; got != want {
		t.Fatalf("member output limit = %d, want %d", got, want)
	}
	if got, want := panelMemberLimits.MaxOutputPerCall, 8192; got != want {
		t.Fatalf("member output-per-call limit = %d, want %d", got, want)
	}
	if got, want := panelMemberLimits.MaxToolCalls, 64; got != want {
		t.Fatalf("member tool limit = %d, want %d", got, want)
	}
	if got, want := panelSynthesisLimits.MaxTurns, 0; got != want {
		t.Fatalf("synthesis max turns = %d, want %d (unlimited)", got, want)
	}
	if got, want := panelSynthesisLimits.MaxPromptTokens, 0; got != want {
		t.Fatalf("synthesis prompt limit = %d, want %d (unlimited)", got, want)
	}
	if got, want := panelSynthesisLimits.MaxOutputTokens, 65536; got != want {
		t.Fatalf("synthesis output limit = %d, want %d", got, want)
	}
	if got, want := panelSynthesisLimits.MaxOutputPerCall, 8192; got != want {
		t.Fatalf("synthesis output-per-call limit = %d, want %d", got, want)
	}
	if got, want := panelSynthesisLimits.MaxToolCalls, 16; got != want {
		t.Fatalf("synthesis tool limit = %d, want %d", got, want)
	}
}
