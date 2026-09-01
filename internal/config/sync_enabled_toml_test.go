package config

import (
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestSyncEnabledThreeStateThroughRealTOML drives the three states of
// `[sync] enabled` through the REAL go-toml decoder.
//
// TestSyncEnabledIsThreeState covers the same three states, but it hands
// resolveSyncConfig a hand-built SyncConfig, so it asserts the author's intent
// about a *bool and never the decode. Everything between the file on disk and
// that struct - the `toml:"enabled"` tag, go-toml's handling of a pointer
// field, the distinction between an absent key and a present false - is
// invisible to it. A wrong or missing tag leaves Enabled nil for BOTH the
// absent key and an explicit `enabled = false`, which silently converts the
// opt-out into a no-op: the user writes the one line the docs give them, and
// the transcript uploads anyway.
//
// The absent-key case is the one that makes the assertion non-trivial. It is
// what distinguishes "the decoder never wrote this field" from "the decoder
// wrote false", and a test with only the true/false rows would pass under a
// tag that never matches anything.
func TestSyncEnabledThreeStateThroughRealTOML(t *testing.T) {
	tests := []struct {
		name string
		toml string
		// wantSet is whether the decoder produced a non-nil pointer at all.
		wantSet      bool
		wantValue    bool
		wantDisabled bool
		// wantActive is what Active reports for a LOGGED-IN user, the only
		// case in which the config value decides anything.
		wantActive bool
	}{
		{
			name:         "explicit false opts out",
			toml:         "[sync]\nenabled = false\n",
			wantSet:      true,
			wantValue:    false,
			wantDisabled: true,
			wantActive:   false,
		},
		{
			name:         "explicit true syncs",
			toml:         "[sync]\nenabled = true\n",
			wantSet:      true,
			wantValue:    true,
			wantDisabled: false,
			wantActive:   true,
		},
		{
			name:         "absent key syncs",
			toml:         "[sync]\ninclude_tool_io = true\n",
			wantSet:      false,
			wantDisabled: false,
			wantActive:   true,
		},
		{
			name:         "absent table syncs",
			toml:         "[provider]\nname = \"deepseek\"\n",
			wantSet:      false,
			wantDisabled: false,
			wantActive:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var file File
			if err := toml.Unmarshal([]byte(tt.toml), &file); err != nil {
				t.Fatalf("parse TOML %q: %v", tt.toml, err)
			}

			if got := file.Sync.Enabled != nil; got != tt.wantSet {
				t.Fatalf("decoded Enabled != nil = %v, want %v; the decoder did not distinguish a present key from an absent one, so the opt-out cannot be expressed", got, tt.wantSet)
			}
			if tt.wantSet && *file.Sync.Enabled != tt.wantValue {
				t.Fatalf("decoded *Enabled = %v, want %v", *file.Sync.Enabled, tt.wantValue)
			}

			cfg := resolveSyncConfig(file.Sync)
			if cfg.Disabled != tt.wantDisabled {
				t.Errorf("Disabled = %v, want %v", cfg.Disabled, tt.wantDisabled)
			}
			if got := cfg.Active(true); got != tt.wantActive {
				t.Errorf("Active(loggedIn=true) = %v, want %v", got, tt.wantActive)
			}
			if cfg.Active(false) {
				t.Error("Active(loggedIn=false) = true, want false; a logged-out CLI must never upload")
			}
		})
	}
}
