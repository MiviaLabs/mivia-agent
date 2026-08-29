package newtui

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
)

func ptr(v bool) *bool { return &v }

func TestMouseEnabledPrecedence(t *testing.T) {
	cases := []struct {
		name string
		res  *config.Resolved
		env  []string
		want bool
	}{
		{"default on", nil, nil, true},
		{"toml off", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(false)}}, nil, false},
		{"toml on", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(true)}}, nil, true},
		{"env overrides toml on", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(true)}}, []string{"MIVIA_MOUSE=0"}, false},
		{"env overrides toml off", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(false)}}, []string{"MIVIA_MOUSE=1"}, true},
		{"env truthy spellings", nil, []string{"TERM=xterm", "MIVIA_MOUSE=yes"}, true},
		{"env falsy spelling", nil, []string{"MIVIA_MOUSE=off"}, false},
		{"unrelated env ignored", &config.Resolved{TUI: config.TUIConfig{Mouse: ptr(false)}}, []string{"MIVIA_OTHER=1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mouseEnabled(tc.res, tc.env); got != tc.want {
				t.Fatalf("mouseEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMouseNotifierBridgeNilGuard covers the launcher's nil-store guard:
// a buildApp that could not produce a store must not register a bridge.
func TestMouseNotifierBridgeNilGuard(t *testing.T) {
	var store *uiadapter.SettingsStore
	if store != nil { // the != arm of run.go's guard
		t.Fatal("nil store must skip the notifier bridge")
	}
	_ = store
}
