package config

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// glmFlash mirrors the shipped z.ai entry that motivated the floor: an
// always-thinking model at "max" effort, whose reasoning reserve (65536) is
// far above any modest [chat] max_tokens an operator would write for a chat
// model.
func glmFlash() ModelSpec {
	return ModelSpec{
		Name:                "glm-5.3-flash",
		ContextWindowTokens: 1048576,
		MaxOutputTokens:     131072,
		Reasoning:           reasoning.Max,
	}
}

// TestExplicitMaxTokensBelowReasoningFloorIsRaised is the regression. A
// user-level [chat] max_tokens = 4096 applies to every workspace that does
// not set its own, including one bound to a model that cannot stop thinking.
// At 4096 the model spends the whole allowance on reasoning tokens and
// returns no answer text, which surfaces as "agent: turn produced no
// assistant text" and looks like a provider fault.
func TestExplicitMaxTokensBelowReasoningFloorIsRaised(t *testing.T) {
	got := EffectiveOutputTokens(glmFlash(), intPtr(4096))
	if got == nil {
		t.Fatal("want a ceiling, got nil")
	}
	if *got != reasoning.OutputReserveFloor(reasoning.Max) {
		t.Fatalf("max_tokens = %d, want the model's reasoning reserve %d", *got, reasoning.OutputReserveFloor(reasoning.Max))
	}
}

// TestExplicitMaxTokensAboveFloorIsAuthoritative pins that the floor only
// ever raises: an operator asking for MORE than the reserve gets exactly
// what they asked for.
func TestExplicitMaxTokensAboveFloorIsAuthoritative(t *testing.T) {
	got := EffectiveOutputTokens(glmFlash(), intPtr(100000))
	if got == nil || *got != 100000 {
		t.Fatalf("max_tokens = %v, want the requested 100000", got)
	}
}

// TestExplicitMaxTokensNonThinkingModelUnchanged pins that a model which
// reserves no more than a plain answer keeps the requested value byte for
// byte. The floor exists for thinking budgets, not as a global minimum.
func TestExplicitMaxTokensNonThinkingModelUnchanged(t *testing.T) {
	for _, level := range []reasoning.Level{"", reasoning.Off} {
		profile := glmFlash()
		profile.Reasoning = level
		got := EffectiveOutputTokens(profile, intPtr(512))
		if got == nil || *got != 512 {
			t.Fatalf("reasoning %q: max_tokens = %v, want the requested 512", level, got)
		}
	}
}

// TestReasoningFloorStaysUnderModelCeiling pins the ordering: the floor is
// applied first, then the model's own max_output_tokens caps it. A floor
// above what the model can emit must never become the request.
func TestReasoningFloorStaysUnderModelCeiling(t *testing.T) {
	profile := glmFlash()
	profile.MaxOutputTokens = 8192
	got := EffectiveOutputTokens(profile, intPtr(4096))
	if got == nil || *got != 8192 {
		t.Fatalf("max_tokens = %v, want the model ceiling 8192", got)
	}
}

// TestReasoningFloorStaysInsideWindow pins that the floor cannot produce a
// reserve larger than the declared context window, which providers reject
// outright.
func TestReasoningFloorStaysInsideWindow(t *testing.T) {
	profile := glmFlash()
	profile.ContextWindowTokens = 16384
	profile.MaxOutputTokens = 131072
	got := EffectiveOutputTokens(profile, intPtr(1024))
	if got == nil || *got != 16384 {
		t.Fatalf("max_tokens = %v, want the context window 16384", got)
	}
}

// TestReasoningFloorPerGradedLevel pins the floor each level contributes, so
// a level whose reserve changes upstream cannot silently stop protecting the
// explicit path.
func TestReasoningFloorPerGradedLevel(t *testing.T) {
	for _, level := range []reasoning.Level{
		reasoning.Minimal, reasoning.Low, reasoning.Medium,
		reasoning.High, reasoning.XHigh, reasoning.Max,
	} {
		profile := glmFlash()
		profile.Reasoning = level
		want := reasoning.OutputReserveFloor(level)
		got := EffectiveOutputTokens(profile, intPtr(256))
		if got == nil || *got != want {
			t.Fatalf("reasoning %q: max_tokens = %v, want %d", level, got, want)
		}
	}
}

// TestUnsetMaxTokensUnchanged pins that the unset path is untouched: it
// already applied the same floor, and the two must agree.
func TestUnsetMaxTokensUnchanged(t *testing.T) {
	got := EffectiveOutputTokens(glmFlash(), nil)
	if got == nil || *got != reasoning.OutputReserveFloor(reasoning.Max) {
		t.Fatalf("unset max_tokens = %v, want the reasoning reserve", got)
	}
}
