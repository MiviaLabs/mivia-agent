package config

import (
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// TestChatConfigRequestTimeoutDecodes verifies the TOML key
// [chat] request_timeout_seconds decodes into the pointer field, and an
// absent key stays nil.
func TestChatConfigRequestTimeoutDecodes(t *testing.T) {
	var present File
	if err := toml.Unmarshal([]byte("[chat]\nrequest_timeout_seconds = 1200"), &present); err != nil {
		t.Fatalf("parse TOML with field: %v", err)
	}
	if present.Chat.RequestTimeoutSeconds == nil {
		t.Fatal("RequestTimeoutSeconds: got nil, want pointer to 1200")
	}
	if got := *present.Chat.RequestTimeoutSeconds; got != 1200 {
		t.Fatalf("RequestTimeoutSeconds: got %d want 1200", got)
	}

	var absent File
	if err := toml.Unmarshal([]byte("[chat]\nmax_tokens = 8192"), &absent); err != nil {
		t.Fatalf("parse TOML without field: %v", err)
	}
	if absent.Chat.RequestTimeoutSeconds != nil {
		t.Fatal("RequestTimeoutSeconds (omitted): got non-nil, want nil")
	}
}

// TestLoadResolvesChatRequestTimeout pins the resolution table for
// [chat] request_timeout_seconds: an unset key and a non-positive value both
// resolve to the compiled default (900s); a positive value resolves to that
// many seconds.
func TestLoadResolvesChatRequestTimeout(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  time.Duration
	}{
		// writeMinimalConfig ends inside the [chat] table, so a bare key
		// continues that table.
		{name: "unset_resolves_default", extra: "", want: DefaultChatRequestTimeoutSeconds * time.Second},
		{name: "explicit_resolves", extra: "request_timeout_seconds = 1200", want: 1200 * time.Second},
		{name: "zero_resolves_default", extra: "request_timeout_seconds = 0", want: DefaultChatRequestTimeoutSeconds * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, tc.extra)})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.ChatRequestTimeout; got != tc.want {
				t.Fatalf("ChatRequestTimeout = %s, want %s", got, tc.want)
			}
		})
	}
}
