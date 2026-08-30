package config

import "testing"

// Every continuation is a full extra provider call on a turn that already
// answered, so the configured bound is normalized rather than trusted: a
// mistyped 500 must not turn one turn into 500 billable requests.
func TestResolveUnactedContinuations(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured int
		want       int
	}{
		{"unset is off", 0, 0},
		{"negative is off", -1, 0},
		{"one passes through", 1, 1},
		{"the ceiling passes through", MaxUnactedContinuationsCeiling, MaxUnactedContinuationsCeiling},
		{"above the ceiling clamps", 500, MaxUnactedContinuationsCeiling},
	} {
		if got := resolveUnactedContinuations(tc.configured); got != tc.want {
			t.Errorf("%s: resolveUnactedContinuations(%d) = %d, want %d", tc.name, tc.configured, got, tc.want)
		}
	}
}

// TestChatMaxUnactedContinuationsResolves pins that the key actually reaches
// Resolved through a real load, not only through the helper above.
func TestChatMaxUnactedContinuationsResolves(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "max_unacted_continuations = 9")})
	if err != nil {
		t.Fatal(err)
	}
	if res.MaxUnactedContinuations != MaxUnactedContinuationsCeiling {
		t.Fatalf("MaxUnactedContinuations = %d, want the clamped ceiling %d", res.MaxUnactedContinuations, MaxUnactedContinuationsCeiling)
	}
	unset, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if unset.MaxUnactedContinuations != 0 {
		t.Fatalf("an unconfigured workspace must leave the mechanism off, got %d", unset.MaxUnactedContinuations)
	}
}
