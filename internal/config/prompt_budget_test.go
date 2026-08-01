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
