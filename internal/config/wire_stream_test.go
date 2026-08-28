package config

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestSubagentConfigWireStreamResolved verifies the wire_stream knob decode
// table. The field is a pointer so an explicit true is distinguishable from
// an absent key: absent and false both resolve off (opt-in, not default -
// see WireStreamResolved's doc comment), true opts in.
func TestSubagentConfigWireStreamResolved(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "absent_resolves_off", raw: "[subagents]\nmax_workers = 2", want: false},
		{name: "true_opts_in", raw: "[subagents]\nwire_stream = true", want: true},
		{name: "false_resolves_off", raw: "[subagents]\nwire_stream = false", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f File
			if err := toml.Unmarshal([]byte(tc.raw), &f); err != nil {
				t.Fatalf("parse TOML: %v", err)
			}
			if got := f.Subagents.WireStreamResolved(); got != tc.want {
				t.Fatalf("WireStreamResolved() = %v, want %v", got, tc.want)
			}
		})
	}
}
