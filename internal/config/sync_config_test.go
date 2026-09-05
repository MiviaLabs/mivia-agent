package config

import (
	"testing"
)

// syncEnabled builds the *bool a TOML `enabled` key resolves to. The nil case
// is written literally at each call site, because "absent" is the state that
// matters and naming it through a helper would hide it.
func syncEnabled(v bool) *bool { return &v }

// TestStreamAssistantOptOutIsExpressible proves the three states are real. A
// plain bool would make "absent" and "false" identical, and since absent now
// means ON, the opt-out would be unreachable.
func TestStreamAssistantOptOutIsExpressible(t *testing.T) {
	off := false
	if resolveSyncConfig(SyncConfig{StreamAssistant: &off}).StreamAssistant {
		t.Error("an explicit stream_assistant = false did not turn streaming off")
	}

	on := true
	if !resolveSyncConfig(SyncConfig{StreamAssistant: &on}).StreamAssistant {
		t.Error("an explicit stream_assistant = true did not turn streaming on")
	}

	if !resolveSyncConfig(SyncConfig{}).StreamAssistant {
		t.Error("an absent stream_assistant key must mean on")
	}
}

func TestSyncConfigDefaultsFailClosed(t *testing.T) {
	cfg := resolveSyncConfig(SyncConfig{})

	// An absent `enabled` key is NOT an opt-out. Sync is gated on being
	// logged in (ResolvedSync.Active), not on a config switch, so the
	// fail-closed default lives in the auth check, not here.
	if cfg.Disabled {
		t.Errorf("Disabled = true, want false (an absent key is not an opt-out)")
	}
	// IncludeToolIO and IncludeThinking were held to a fail-closed rule, on
	// the reasoning that they decide WHETHER content leaves the machine while
	// StreamAssistant decides only HOW. That split does not survive contact
	// with the product: sync runs only for a logged-in user who asked for the
	// remote viewer, and a viewer that silently omits the agent's reasoning -
	// or what its tools read and wrote - is not showing the session.
	// Off-by-default also failed SILENTLY: the transcript simply had no
	// reasoning in it, with nothing saying any had been withheld, which is
	// how it was reported from production.
	//
	// The activation gate is unchanged and still fail-closed: a logged-out
	// CLI uploads nothing (ResolvedSync.Active), and `enabled = false` opts
	// the whole thing out. Within an ALREADY-ACTIVE sync the session now
	// streams in full unless the user says otherwise, and every opt-out stays
	// one explicit `false` away - which is why these are pointers.
	if !cfg.IncludeToolIO {
		t.Errorf("IncludeToolIO = false, want true (absent key means on)")
	}
	if !cfg.IncludeThinking {
		t.Errorf("IncludeThinking = false, want true (absent key means on)")
	}
	if !cfg.StreamAssistant {
		t.Errorf("StreamAssistant = false, want true (absent key means on)")
	}
	if cfg.PollWaitSeconds != 25 {
		t.Errorf("PollWaitSeconds = %d, want 25", cfg.PollWaitSeconds)
	}
	if cfg.HeartbeatSeconds != 30 {
		t.Errorf("HeartbeatSeconds = %d, want 30", cfg.HeartbeatSeconds)
	}
	if cfg.MaxUnflushed != 5000 {
		t.Errorf("MaxUnflushed = %d, want 5000", cfg.MaxUnflushed)
	}
}

// TestSyncEnabledIsThreeState is the whole reason the file form holds a
// pointer. An absent key and an explicit false must not resolve to the same
// thing: the first means "sync when logged in", the second means "never".
// A plain bool collapses them and makes the opt-out impossible to express.
func TestSyncEnabledIsThreeState(t *testing.T) {
	tests := []struct {
		name         string
		enabled      *bool
		wantDisabled bool
		// wantActive is what Active reports for a LOGGED-IN user, which is
		// the only case where the config value decides anything.
		wantActive bool
	}{
		{"absent key syncs", nil, false, true},
		{"explicit true syncs", syncEnabled(true), false, true},
		{"explicit false opts out", syncEnabled(false), true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := resolveSyncConfig(SyncConfig{Enabled: tt.enabled})
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

func TestSyncConfigPreservesCustomValues(t *testing.T) {
	custom := SyncConfig{
		Enabled:          syncEnabled(true),
		IncludeToolIO:    syncEnabled(true),
		IncludeThinking:  syncEnabled(true),
		APIURL:           "https://sync.mivia.ai",
		PollWaitSeconds:  15,
		HeartbeatSeconds: 45,
		MaxUnflushed:     1000,
	}
	cfg := resolveSyncConfig(custom)

	if cfg.Disabled || !cfg.IncludeToolIO || !cfg.IncludeThinking {
		t.Errorf("expected custom bool flags preserved, got %+v", cfg)
	}
	if cfg.APIURL != "https://sync.mivia.ai" {
		t.Errorf("APIURL = %q", cfg.APIURL)
	}
	if cfg.PollWaitSeconds != 15 || cfg.HeartbeatSeconds != 45 || cfg.MaxUnflushed != 1000 {
		t.Errorf("expected custom bounds preserved, got %+v", cfg)
	}
}

// TestSyncAPIURLValidation pins the transcript-upload endpoint's URL policy.
// sync.api_url is attacker-editable config that redirects every message of
// every conversation, so it gets the miviaauth shape (https, or a literal
// loopback host for local API development) and NOT validateBaseURL's
// MIVIA_ALLOW_INSECURE_HTTP relaxation.
//
// An empty api_url is accepted: no user is asked to configure sync, and the
// wiring falls back to the API root internal/miviaauth resolves.
func TestSyncAPIURLValidation(t *testing.T) {
	tests := []struct {
		name    string
		sync    ResolvedSync
		wantErr bool
	}{
		{"opted out with no url is not validated", ResolvedSync{Disabled: true}, false},
		{"opted out with a bad url is not validated", ResolvedSync{Disabled: true, APIURL: "http://evil.example.com"}, false},
		{"no url falls back to the API root", ResolvedSync{}, false},
		{"blank url falls back to the API root", ResolvedSync{APIURL: "   "}, false},
		{"https is accepted", ResolvedSync{APIURL: "https://api.mivia.app"}, false},
		{"plain http is rejected", ResolvedSync{APIURL: "http://evil.example.com"}, true},
		{"http loopback is accepted", ResolvedSync{APIURL: "http://localhost:3001"}, false},
		{"http 127.0.0.1 is accepted", ResolvedSync{APIURL: "http://127.0.0.1:3001"}, false},
		{"a non-URL is rejected", ResolvedSync{APIURL: "not a url"}, true},
		{"userinfo is rejected", ResolvedSync{APIURL: "https://user:pass@api.mivia.app"}, true},
		{"a non-http scheme is rejected", ResolvedSync{APIURL: "file:///etc/passwd"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Resolved{ProviderName: "deepseek", Model: "model", BaseURL: "https://example.test", APIKeyEnv: "KEY", PromptCache: "auto", Tools: validToolsForSyncTest(), Sync: tt.sync}
			err := res.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

// TestSyncAPIURLIgnoresAllowInsecureHTTP keeps the relaxation that provider
// base_url carries out of the transcript-upload path: an env var must not be
// able to redirect conversation content onto a cleartext non-loopback host.
func TestSyncAPIURLIgnoresAllowInsecureHTTP(t *testing.T) {
	t.Setenv("MIVIA_ALLOW_INSECURE_HTTP", "1")
	res := Resolved{
		ProviderName: "deepseek", Model: "model", BaseURL: "https://example.test",
		APIKeyEnv: "KEY", PromptCache: "auto", Tools: validToolsForSyncTest(),
		Sync: ResolvedSync{APIURL: "http://evil.example.com"},
	}
	if err := res.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want rejection despite MIVIA_ALLOW_INSECURE_HTTP=1")
	}
}

// validToolsForSyncTest supplies the unrelated [tools] bounds Validate also
// checks, so a sync case fails on the sync rule or not at all.
func validToolsForSyncTest() ToolsConfig {
	return ToolsConfig{MaxInspectRepositoryBytes: 64 << 10, MaxTavilyResponseBytes: 4 << 20}
}

// TestSyncConfigBackgroundWatchMaxCustomValuePreserved covers the positive
// branch of the BackgroundWatchMax default: a configured value greater than
// zero must be carried through as-is rather than overwritten by the built-in
// default of 8.
func TestSyncConfigBackgroundWatchMaxCustomValuePreserved(t *testing.T) {
	cfg := resolveSyncConfig(SyncConfig{BackgroundWatchMax: 3})
	if cfg.BackgroundWatchMax != 3 {
		t.Errorf("BackgroundWatchMax = %d, want 3 (configured value should be preserved, not defaulted)", cfg.BackgroundWatchMax)
	}
}

// TestSyncConfigBackgroundWatchMaxDefaultsToEight covers the else branch: an
// absent (zero-value) BackgroundWatchMax falls back to the built-in default.
func TestSyncConfigBackgroundWatchMaxDefaultsToEight(t *testing.T) {
	cfg := resolveSyncConfig(SyncConfig{})
	if cfg.BackgroundWatchMax != 8 {
		t.Errorf("BackgroundWatchMax = %d, want 8 (default)", cfg.BackgroundWatchMax)
	}
}

// TestStreamingDefaultsOnWhenSyncIsOn pins the rule that remote sync streams
// the session IN FULL unless the user says otherwise. include_thinking and
// include_tool_io were plain bools, so an absent key read as OFF: every user
// who had never heard of the keys got a remote transcript with no reasoning
// in it, and no indication any had been withheld. Fails against a plain bool,
// where absent and "false" are the same state.
func TestStreamingDefaultsOnWhenSyncIsOn(t *testing.T) {
	absent := resolveSyncConfig(SyncConfig{})
	if !absent.IncludeThinking {
		t.Error("IncludeThinking = false for an absent key, want true: sync streams in full unless opted out")
	}
	if !absent.IncludeToolIO {
		t.Error("IncludeToolIO = false for an absent key, want true")
	}
	if !absent.StreamAssistant {
		t.Error("StreamAssistant = false for an absent key, want true")
	}

	// The opt-out must stay reachable, which is the whole reason these are
	// pointers rather than bools.
	off := resolveSyncConfig(SyncConfig{
		IncludeThinking: syncEnabled(false),
		IncludeToolIO:   syncEnabled(false),
		StreamAssistant: syncEnabled(false),
	})
	if off.IncludeThinking || off.IncludeToolIO || off.StreamAssistant {
		t.Errorf("an explicit false did not turn streaming off: %+v", off)
	}
}
