package controller

import "testing"

func TestPanelWorkLimitSlicesAreFixed(t *testing.T) {
	if got, want := panelMemberLimits.MaxTurns, 16; got != want {
		t.Fatalf("member max turns = %d, want %d", got, want)
	}
	if got, want := panelMemberLimits.MaxPromptTokens, 524288; got != want {
		t.Fatalf("member prompt limit = %d, want %d", got, want)
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
	if got, want := panelSynthesisLimits.MaxTurns, 8; got != want {
		t.Fatalf("synthesis max turns = %d, want %d", got, want)
	}
	if got, want := panelSynthesisLimits.MaxPromptTokens, 524288; got != want {
		t.Fatalf("synthesis prompt limit = %d, want %d", got, want)
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
