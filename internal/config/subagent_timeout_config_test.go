package config

import "testing"

// TestLoadSubagentTotalTimeoutKnob pins the raw TOML decode of
// [subagents] default_total_timeout_seconds: unset and 0 reach the code as
// 0 (the compiled 3600s default applies at resolution time), and a negative
// literal survives decode intact so the documented operator opt-out works.
func TestLoadSubagentTotalTimeoutKnob(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  int
	}{
		{name: "unset_decodes_zero", extra: "", want: 0},
		{name: "zero_decodes_zero", extra: "\n[subagents]\ndefault_total_timeout_seconds = 0\n", want: 0},
		{name: "negative_survives_decode", extra: "\n[subagents]\ndefault_total_timeout_seconds = -1\n", want: -1},
		{name: "positive_survives_decode", extra: "\n[subagents]\ndefault_total_timeout_seconds = 45\n", want: 45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, tc.extra)})
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if res.Subagents.DefaultTotalTimeoutSec != tc.want {
				t.Fatalf("DefaultTotalTimeoutSec = %d, want %d", res.Subagents.DefaultTotalTimeoutSec, tc.want)
			}
		})
	}
}
