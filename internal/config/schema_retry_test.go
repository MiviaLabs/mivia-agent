package config

import (
	"strconv"
	"testing"
)

// schema_retry_max is the one [subagents] knob with a load-time ceiling. A
// positive value above the cap is an operator typo (40 typed instead of 4), and
// honoring it would configure a 40+-call schema-retry storm: each retry is a
// full provider round-trip. Values above MaxSchemaRetryMax clamp to the cap;
// <= 0 keeps the existing "0 = use the default 2" behavior.
func TestSchemaRetryMaxClampedAtLoad(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  int
	}{
		{name: "typo 41 clamps to cap", value: 41, want: MaxSchemaRetryMax},
		{name: "cap passes through", value: MaxSchemaRetryMax, want: MaxSchemaRetryMax},
		{name: "sane value passes through", value: 4, want: 4},
		{name: "zero defaults to 2", value: 0, want: 2},
		{name: "negative defaults to 2", value: -7, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeMinimalConfig(t, "[subagents]\nschema_retry_max = "+strconv.Itoa(tt.value)+"\n")
			res, err := Load(LoadOptions{ConfigPath: path})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Subagents.SchemaRetryMax; got != tt.want {
				t.Fatalf("schema_retry_max = %d, want %d", got, tt.want)
			}
		})
	}
}
