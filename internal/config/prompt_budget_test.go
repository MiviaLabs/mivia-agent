package config

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

func ptr(v int) *int { return &v }

// TestEffectiveOutputTokensNilAndZero covers the nil-return and
// both-zero branches: when neither the model profile nor the session
// request configures a positive ceiling, the function returns nil.
func TestEffectiveOutputTokensNilAndZero(t *testing.T) {
	tests := []struct {
		name      string
		profile   ModelSpec
		requested *int
		want      *int
	}{
		{name: "both zero nil requested", profile: ModelSpec{MaxOutputTokens: 0}, requested: nil, want: nil},
		{name: "both zero requested pointer to zero", profile: ModelSpec{MaxOutputTokens: 0}, requested: ptr(0), want: nil},
		{name: "negative profile treated as zero", profile: ModelSpec{MaxOutputTokens: -1}, requested: nil, want: nil},
		{name: "negative requested ignored no model", profile: ModelSpec{MaxOutputTokens: 0}, requested: ptr(-1), want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveOutputTokens(tt.profile, tt.requested)
			if got != nil {
				t.Fatalf("EffectiveOutputTokens() = %d, want nil", *got)
			}
		})
	}
}

// TestEffectiveOutputTokensTighterOfTwo covers the positive-return
// branches: the function picks the tighter of the model ceiling and
// the session request, handling negative inputs as zero along the way.
func TestEffectiveOutputTokensTighterOfTwo(t *testing.T) {
	tests := []struct {
		name      string
		profile   ModelSpec
		requested *int
		want      *int
	}{
		{name: "model ceiling only", profile: ModelSpec{MaxOutputTokens: 4096}, requested: nil, want: ptr(4096)},
		{name: "requested only", profile: ModelSpec{MaxOutputTokens: 0}, requested: ptr(8192), want: ptr(8192)},
		{name: "model ceiling tighter", profile: ModelSpec{MaxOutputTokens: 4096}, requested: ptr(16384), want: ptr(4096)},
		{name: "requested tighter", profile: ModelSpec{MaxOutputTokens: 16384}, requested: ptr(4096), want: ptr(4096)},
		{name: "equal values", profile: ModelSpec{MaxOutputTokens: 8192}, requested: ptr(8192), want: ptr(8192)},
		{name: "negative profile falls to requested", profile: ModelSpec{MaxOutputTokens: -1}, requested: ptr(4096), want: ptr(4096)},
		{name: "negative requested falls to model", profile: ModelSpec{MaxOutputTokens: 4096}, requested: ptr(-1), want: ptr(4096)},
		{name: "large context", profile: ModelSpec{MaxOutputTokens: 16384, ContextWindowTokens: 1000000}, requested: nil, want: ptr(16384)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveOutputTokens(tt.profile, tt.requested)
			if got == nil {
				t.Fatal("EffectiveOutputTokens() = nil, want non-nil")
			}
			if *got != *tt.want {
				t.Fatalf("EffectiveOutputTokens() = %d, want %d", *got, *tt.want)
			}
		})
	}
}

// TestEffectiveOutputTokensCapsUnrequestedReserveAtDefault pins the fix for a
// real production failure: a claude-sonnet-5 session (200k window, 128k model
// ceiling, [chat] max_tokens unset) got only 200000-128000 = 72000 tokens of
// usable prompt, so compaction fired at 57,600 - 29% of the window the user
// believed they had. max_output_tokens is a per-response CEILING, not a
// typical response size; using it as the DEFAULT reserve both asks the
// provider for an absurd response on every turn and permanently removes that
// much prompt capacity. An unrequested reserve is now capped at
// DefaultOutputReserveTokens.
func TestEffectiveOutputTokensCapsUnrequestedReserveAtDefault(t *testing.T) {
	got := EffectiveOutputTokens(ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000}, nil)
	if got == nil {
		t.Fatal("EffectiveOutputTokens() = nil, want a capped reserve")
	}
	if *got != DefaultOutputReserveTokens {
		t.Fatalf("EffectiveOutputTokens() = %d, want the default cap %d", *got, DefaultOutputReserveTokens)
	}
}

// TestEffectiveOutputTokensReservesFloorWithNoDeclaredCeiling pins a second
// review-round finding: an undeclared max_output_tokens (ceiling<=0, a valid
// config shape - deepseek-v4-pro, gpt-oss:20b, and tencent/hy3-preview all
// ship with reasoning set and no max_output_tokens in .mivia/mivia.toml)
// used to return nil regardless of reasoning level, meaning
// EffectivePromptTokens reserved NOTHING for the completion. But the wire
// layer (effectiveMaxTokens/anthropicMaxTokens) does not consult
// profile.MaxOutputTokens at all - it applies reasoning.OutputReserveFloor
// unconditionally whenever MaxTokens is unset, purely from the resolved
// reasoning level. So an active reasoning level with no declared ceiling
// must still reserve reasoning.OutputReserveFloor(level), not nil.
func TestEffectiveOutputTokensReservesFloorWithNoDeclaredCeiling(t *testing.T) {
	profile := ModelSpec{ContextWindowTokens: 1100000, MaxOutputTokens: 0, Reasoning: reasoning.High}
	got := EffectiveOutputTokens(profile, nil)
	if got == nil {
		t.Fatal("EffectiveOutputTokens() = nil, want reasoning.OutputReserveFloor(High) even with no declared ceiling")
	}
	want := reasoning.OutputReserveFloor(reasoning.High)
	if *got != want {
		t.Fatalf("EffectiveOutputTokens() = %d, want %d", *got, want)
	}
}

// TestEffectiveOutputTokensNoDeclaredCeilingNoReasoningStaysNil guards the
// no-regression direction: a profile with no declared ceiling AND no
// configured reasoning level must keep returning nil exactly as before -
// this fix is scoped to the reasoning-active gap, not a blanket change to
// the undeclared-ceiling contract other callers may rely on (nil meaning
// "no ceiling applies").
func TestEffectiveOutputTokensNoDeclaredCeilingNoReasoningStaysNil(t *testing.T) {
	profile := ModelSpec{ContextWindowTokens: 32768, MaxOutputTokens: 0}
	if got := EffectiveOutputTokens(profile, nil); got != nil {
		t.Fatalf("EffectiveOutputTokens() = %d, want nil", *got)
	}
}

// TestEffectiveOutputTokensRaisesUnrequestedReserveForHighReasoningEffort
// pins the real fix for the planner/wire mismatch: internal/provider's wire
// layer (effectiveMaxTokens in openai_compat_request.go) sends up to
// reasoning.OutputReserveFloor(level) as max_tokens whenever a request
// leaves MaxTokens unset - for z.ai's GLM-5.3 family at "max" effort
// (always-thinking, per .mivia/mivia.toml) that is 65536, not the flat
// DefaultOutputReserveTokens=32768 this function used to cap every unset
// reserve at regardless of reasoning level. Reserving only 32768 here would
// let EffectivePromptTokens hand the planner a Budget that assumes 32768 of
// completion headroom, while the wire request separately asks for 65536 on
// top of it - a prompt_tokens+max_tokens over-context-window rejection this
// function has every input needed to avoid.
func TestEffectiveOutputTokensRaisesUnrequestedReserveForHighReasoningEffort(t *testing.T) {
	profile := ModelSpec{ContextWindowTokens: 1048576, MaxOutputTokens: 131072, Reasoning: reasoning.Max}
	got := EffectiveOutputTokens(profile, nil)
	if got == nil {
		t.Fatal("EffectiveOutputTokens() = nil, want a reasoning-scaled reserve")
	}
	want := reasoning.OutputReserveFloor(reasoning.Max)
	if want <= DefaultOutputReserveTokens {
		t.Fatalf("test setup: reasoning.OutputReserveFloor(Max)=%d must exceed DefaultOutputReserveTokens=%d for this test to be meaningful", want, DefaultOutputReserveTokens)
	}
	if *got != want {
		t.Fatalf("EffectiveOutputTokens() = %d, want %d (reasoning.OutputReserveFloor(Max), not the flat default)", *got, want)
	}
}

// TestEffectiveOutputTokensKeepsDefaultForLowReasoningEffort guards the
// other direction: a low-effort or non-reasoning model must not regress to
// reserving MORE than DefaultOutputReserveTokens just because this function
// now consults the reasoning level - reasoning.OutputReserveFloor for Off/Low
// stays at or below the default, so the max(...) in EffectiveOutputTokens
// must still yield exactly DefaultOutputReserveTokens for those levels,
// unchanged from before this fix.
func TestEffectiveOutputTokensKeepsDefaultForLowReasoningEffort(t *testing.T) {
	for _, level := range []reasoning.Level{"", reasoning.Off, reasoning.Low} {
		profile := ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000, Reasoning: level}
		got := EffectiveOutputTokens(profile, nil)
		if got == nil {
			t.Fatalf("level %q: EffectiveOutputTokens() = nil, want the capped default", level)
		}
		if *got != DefaultOutputReserveTokens {
			t.Fatalf("level %q: EffectiveOutputTokens() = %d, want the unchanged default %d", level, *got, DefaultOutputReserveTokens)
		}
	}
}

// TestEffectiveOutputTokensHonorsExplicitRequestAboveDefault pins a latent
// second defect the cap exposes: the old code only honored an explicit
// [chat] max_tokens when it was SMALLER than the model ceiling, so an
// operator could never raise the reserve. With a default cap in place that
// would make a large explicit request unreachable, so explicit intent is now
// authoritative up to the model ceiling.
func TestEffectiveOutputTokensHonorsExplicitRequestAboveDefault(t *testing.T) {
	got := EffectiveOutputTokens(ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000}, ptr(100000))
	if got == nil {
		t.Fatal("EffectiveOutputTokens() = nil, want the explicit request")
	}
	if *got != 100000 {
		t.Fatalf("EffectiveOutputTokens() = %d, want the explicit 100000", *got)
	}
}

// TestEffectiveOutputTokensClampsExplicitRequestToModelCeiling: explicit
// intent is authoritative, but never beyond what the model can produce.
func TestEffectiveOutputTokensClampsExplicitRequestToModelCeiling(t *testing.T) {
	got := EffectiveOutputTokens(ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000}, ptr(500000))
	if got == nil {
		t.Fatal("EffectiveOutputTokens() = nil, want the model ceiling")
	}
	if *got != 128000 {
		t.Fatalf("EffectiveOutputTokens() = %d, want the model ceiling 128000", *got)
	}
}

// TestEffectivePromptTokensClaudeShapedProfile is the end-to-end regression
// pin for the production failure: the same profile that yielded 72000 must
// now yield window minus the capped default reserve.
func TestEffectivePromptTokensClaudeShapedProfile(t *testing.T) {
	got := EffectivePromptTokens(ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000}, nil, DefaultPromptCapTokens, 0)
	want := 200000 - DefaultOutputReserveTokens
	if got != want {
		t.Fatalf("EffectivePromptTokens() = %d, want %d", got, want)
	}
}

// TestEffectivePromptTokensGlmMaxEffortShapedProfile is the end-to-end
// regression pin for glm-5.3-flash's actual .mivia/mivia.toml entry
// (context_window_tokens=1048576, max_output_tokens=131072, reasoning='max',
// always-thinking): the prompt budget must exclude
// reasoning.OutputReserveFloor(Max)=65536, not the flat 32768 default, so a
// caller planning history against this Budget agrees with what the wire
// request (effectiveMaxTokens) will actually ask the provider for.
func TestEffectivePromptTokensGlmMaxEffortShapedProfile(t *testing.T) {
	profile := ModelSpec{ContextWindowTokens: 1048576, MaxOutputTokens: 131072, Reasoning: reasoning.Max}
	got := EffectivePromptTokens(profile, nil, 0, 0)
	want := 1048576 - reasoning.OutputReserveFloor(reasoning.Max)
	if got != want {
		t.Fatalf("EffectivePromptTokens() = %d, want %d", got, want)
	}
}

// TestEffectivePromptTokensNoDeclaredCeilingShapedProfile is the end-to-end
// regression pin for the shipped deepseek-v4-pro entry
// (.mivia/mivia.toml: context_window_tokens=1100000, reasoning='high', no
// max_output_tokens): the prompt budget must still exclude
// reasoning.OutputReserveFloor(High)=32768 even with no declared ceiling,
// since the wire layer sends that same max_tokens regardless.
func TestEffectivePromptTokensNoDeclaredCeilingShapedProfile(t *testing.T) {
	profile := ModelSpec{ContextWindowTokens: 1100000, Reasoning: reasoning.High}
	got := EffectivePromptTokens(profile, nil, 0, 0)
	want := 1100000 - reasoning.OutputReserveFloor(reasoning.High)
	if got != want {
		t.Fatalf("EffectivePromptTokens() = %d, want %d", got, want)
	}
}

// TestEffectivePromptTokensReserveNeverExceedsWindow guards the provider-side
// invariant the cap must not break: Anthropic (and others) validate
// input_tokens + max_tokens <= context_window and reject outright, so the
// reserve subtracted from the budget and the max_tokens sent on the wire must
// stay in lockstep and always fit.
func TestEffectivePromptTokensReserveNeverExceedsWindow(t *testing.T) {
	profiles := []ModelSpec{
		{ContextWindowTokens: 200000, MaxOutputTokens: 128000},                            // claude / glm
		{ContextWindowTokens: 1000000, MaxOutputTokens: 384000},                           // deepseek
		{ContextWindowTokens: 1000000, MaxOutputTokens: 65536},                            // gemini
		{ContextWindowTokens: 131072, MaxOutputTokens: 32768},                             // gpt-oss-120b
		{ContextWindowTokens: 32768},                                                      // no declared ceiling
		{ContextWindowTokens: 5, MaxOutputTokens: 10},                                     // degenerate
		{ContextWindowTokens: 1048576, MaxOutputTokens: 131072, Reasoning: reasoning.Max}, // glm-5.3-flash, max effort
		{ContextWindowTokens: 1100000, Reasoning: reasoning.High},                         // deepseek-v4-pro, no declared ceiling
	}
	for _, profile := range profiles {
		budget := EffectivePromptTokens(profile, nil, 0, 0)
		reserve := 0
		if r := EffectiveOutputTokens(profile, nil); r != nil {
			reserve = *r
		}
		if profile.ContextWindowTokens > 0 && budget+reserve > profile.ContextWindowTokens {
			t.Fatalf("profile %+v: budget %d + reserve %d exceeds window %d", profile, budget, reserve, profile.ContextWindowTokens)
		}
	}
}

type promptBudgetCase struct {
	name        string
	profile     ModelSpec
	maxTokens   *int
	operatorCap int
	requested   int
	want        int
}

// TestEffectivePromptTokensFailsClosed covers the never-negative contract: a
// completion reserve at or above the window leaves no prompt capacity and
// must yield 0, never a negative budget (it used to return -256000).
//
// Every case here drives an EXPLICIT max_tokens, because an unset request is
// now capped at DefaultOutputReserveTokens and can no longer reach the
// window - explicit intent is the only path that still reaches these bounds.
func TestEffectivePromptTokensFailsClosed(t *testing.T) {
	tests := []promptBudgetCase{
		{
			name:      "reserve exceeds window is zero not negative",
			profile:   ModelSpec{ContextWindowTokens: 128000, MaxOutputTokens: 384000},
			maxTokens: ptr(384000),
			want:      0,
		},
		{
			name:      "reserve equals window is zero",
			profile:   ModelSpec{ContextWindowTokens: 128000, MaxOutputTokens: 128000},
			maxTokens: ptr(128000),
			want:      0,
		},
		{
			// No declared window means no clamp target, so the explicit
			// request stands and consumes the whole fallback capacity.
			name:      "reserve above unknown fallback is zero not negative",
			profile:   ModelSpec{ContextWindowTokens: 0, MaxOutputTokens: UnknownContextWindowTokens + 1},
			maxTokens: ptr(UnknownContextWindowTokens + 1),
			want:      0,
		},
		{
			// Hand-built sub-floor window: the reserve is clamped down to the
			// window, leaving nothing.
			name:    "tiny window with reserve clamps to zero",
			profile: ModelSpec{ContextWindowTokens: 5, MaxOutputTokens: 10},
			want:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectivePromptTokens(tt.profile, tt.maxTokens, tt.operatorCap, tt.requested)
			if got != tt.want {
				t.Fatalf("EffectivePromptTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestEffectivePromptTokensDerivesAndCaps covers the ordinary arithmetic:
// window minus reserve, the undeclared-window fallback, and the two
// caps (operator and session-requested) that override the derived budget.
func TestEffectivePromptTokensDerivesAndCaps(t *testing.T) {
	tests := []promptBudgetCase{
		{
			// Window minus reserve for an explicit operator request; the
			// unset case is TestEffectivePromptTokensClaudeShapedProfile.
			name:      "window minus reserve unchanged",
			profile:   ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000},
			maxTokens: ptr(128000),
			want:      72000,
		},
		{
			// Validated config never reaches this: load rejects windows
			// below 1024.
			name:    "no declared window falls back to 128k default",
			profile: ModelSpec{ContextWindowTokens: 0},
			want:    UnknownContextWindowTokens,
		},
		{
			name:        "operator cap tighter than derived",
			profile:     ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000},
			operatorCap: 50000,
			want:        50000,
		},
		{
			name:      "requested tighter than derived",
			profile:   ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000},
			requested: 30000,
			want:      30000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectivePromptTokens(tt.profile, tt.maxTokens, tt.operatorCap, tt.requested)
			if got != tt.want {
				t.Fatalf("EffectivePromptTokens() = %d, want %d", got, tt.want)
			}
		})
	}
}
