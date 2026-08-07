package config

import "testing"

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

// TestEffectivePromptTokens covers the fail-closed budget contract:
// (a) a completion reserve that exceeds the window must yield 0 (it used to
// return -256000); (b) a reserve that consumes the whole window yields 0;
// (c) a reserve below the window yields window-reserve unchanged; (d) a
// profile with no declared window falls back to the documented legacy default
// maxContextWindowTokens; (e) a tighter operator cap wins over the derived
// budget; (f) a tighter session-requested cap wins over the derived budget.
func TestEffectivePromptTokens(t *testing.T) {
	tests := []struct {
		name        string
		profile     ModelSpec
		maxTokens   *int
		operatorCap int
		requested   int
		want        int
	}{
		{
			// (a) reserve (384000) >= window (128000): was -256000, now 0.
			name:    "reserve exceeds window is zero not negative",
			profile: ModelSpec{ContextWindowTokens: 128000, MaxOutputTokens: 384000},
			want:    0,
		},
		{
			// (b) reserve == window: nothing left for the prompt, fail closed.
			name:    "reserve equals window is zero",
			profile: ModelSpec{ContextWindowTokens: 128000, MaxOutputTokens: 128000},
			want:    0,
		},
		{
			// (c) unchanged arithmetic: window minus reserve.
			name:    "window minus reserve unchanged",
			profile: ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000},
			want:    72000,
		},
		{
			// (d) legacy no-declared-window default (validated config never
			// reaches it: load rejects windows below 1024).
			name:    "no declared window falls back to legacy default",
			profile: ModelSpec{ContextWindowTokens: 0},
			want:    maxContextWindowTokens,
		},
		{
			// (e) operator cap tighter than the derived 72000.
			name:        "operator cap tighter than derived",
			profile:     ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000},
			operatorCap: 50000,
			want:        50000,
		},
		{
			// (f) session-requested cap tighter than the derived 72000.
			name:      "requested tighter than derived",
			profile:   ModelSpec{ContextWindowTokens: 200000, MaxOutputTokens: 128000},
			requested: 30000,
			want:      30000,
		},
		{
			// Reserve above the legacy fallback window: 0, never negative.
			name:    "reserve above legacy fallback is zero not negative",
			profile: ModelSpec{ContextWindowTokens: 0, MaxOutputTokens: maxContextWindowTokens + 1},
			want:    0,
		},
		{
			// Hand-built sub-floor window still clamps at 0, never negative.
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
