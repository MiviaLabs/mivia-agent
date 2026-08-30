package config

import (
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// TestProviderSectionStreamContentIdleTimeoutDecodes verifies the TOML key
// [provider] stream_content_idle_timeout_seconds decodes into the pointer
// field, and an absent key stays nil.
func TestProviderSectionStreamContentIdleTimeoutDecodes(t *testing.T) {
	var present File
	if err := toml.Unmarshal([]byte("[provider]\nstream_content_idle_timeout_seconds = 45"), &present); err != nil {
		t.Fatalf("parse TOML with field: %v", err)
	}
	if present.Provider.StreamContentIdleTimeoutSeconds == nil {
		t.Fatal("StreamContentIdleTimeoutSeconds: got nil, want pointer to 45")
	}
	if got := *present.Provider.StreamContentIdleTimeoutSeconds; got != 45 {
		t.Fatalf("StreamContentIdleTimeoutSeconds: got %d want 45", got)
	}

	var absent File
	if err := toml.Unmarshal([]byte("[provider]\nname = \"deepseek\""), &absent); err != nil {
		t.Fatalf("parse TOML without field: %v", err)
	}
	if absent.Provider.StreamContentIdleTimeoutSeconds != nil {
		t.Fatal("StreamContentIdleTimeoutSeconds (omitted): got non-nil, want nil")
	}
}

// TestLoadResolvesStreamContentIdleTimeout pins the resolution table for
// [provider] stream_content_idle_timeout_seconds: an unset key and a
// non-positive value both resolve to the compiled default (90s); a positive
// value resolves to that many seconds.
func TestLoadResolvesStreamContentIdleTimeout(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  time.Duration
	}{
		{name: "unset_resolves_default", extra: "", want: DefaultStreamContentIdleTimeoutSeconds * time.Second},
		{name: "explicit_resolves", extra: "stream_content_idle_timeout_seconds = 45", want: 45 * time.Second},
		{name: "zero_resolves_default", extra: "stream_content_idle_timeout_seconds = 0", want: DefaultStreamContentIdleTimeoutSeconds * time.Second},
		{name: "negative_resolves_default", extra: "stream_content_idle_timeout_seconds = -5", want: DefaultStreamContentIdleTimeoutSeconds * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Load(LoadOptions{ConfigPath: minimalDeepSeekConfig(t, tc.extra)})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.StreamContentIdleTimeout; got != tc.want {
				t.Fatalf("StreamContentIdleTimeout = %s, want %s", got, tc.want)
			}
		})
	}
}
