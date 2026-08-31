package config

import (
	"testing"
)

// syncEnabled builds the *bool a TOML `enabled` key resolves to. The nil case
// is written literally at each call site, because "absent" is the state that
// matters and naming it through a helper would hide it.
func syncEnabled(v bool) *bool { return &v }

func TestSyncConfigDefaultsFailClosed(t *testing.T) {
	cfg := resolveSyncConfig(SyncConfig{})

	// An absent `enabled` key is NOT an opt-out. Sync is gated on being
	// logged in (ResolvedSync.Active), not on a config switch, so the
	// fail-closed default lives in the auth check, not here.
	if cfg.Disabled {
		t.Errorf("Disabled = true, want false (an absent key is not an opt-out)")
	}
	if cfg.IncludeToolIO {
		t.Errorf("IncludeToolIO = true, want false (fail-closed)")
	}
	if cfg.IncludeThinking {
		t.Errorf("IncludeThinking = true, want false (fail-closed)")
	}
	// Contract T2: streaming off by default. A durable transcript wants one
	// settled message per turn, not the partial states a live view needs.
	if cfg.StreamAssistant {
		t.Errorf("StreamAssistant = true, want false (contract T2: streaming off)")
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
		IncludeToolIO:    true,
		IncludeThinking:  true,
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
