package config

import (
	"testing"
)

func TestSyncConfigDefaultsFailClosed(t *testing.T) {
	cfg := resolveSyncConfig(SyncConfig{})

	if cfg.Enabled {
		t.Errorf("Enabled = true, want false (fail-closed)")
	}
	if cfg.IncludeToolIO {
		t.Errorf("IncludeToolIO = true, want false (fail-closed)")
	}
	if cfg.IncludeThinking {
		t.Errorf("IncludeThinking = true, want false (fail-closed)")
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

func TestSyncConfigPreservesCustomValues(t *testing.T) {
	custom := SyncConfig{
		Enabled:          true,
		IncludeToolIO:    true,
		IncludeThinking:  true,
		APIURL:           "https://sync.mivia.ai",
		PollWaitSeconds:  15,
		HeartbeatSeconds: 45,
		MaxUnflushed:     1000,
	}
	cfg := resolveSyncConfig(custom)

	if !cfg.Enabled || !cfg.IncludeToolIO || !cfg.IncludeThinking {
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
func TestSyncAPIURLValidation(t *testing.T) {
	tests := []struct {
		name    string
		sync    SyncConfig
		wantErr bool
	}{
		{"disabled with no url is not validated", SyncConfig{Enabled: false}, false},
		{"disabled with a bad url is not validated", SyncConfig{Enabled: false, APIURL: "http://evil.example.com"}, false},
		{"enabled with no url is an error", SyncConfig{Enabled: true}, true},
		{"enabled with https is accepted", SyncConfig{Enabled: true, APIURL: "https://api.mivia.app"}, false},
		{"enabled with plain http is rejected", SyncConfig{Enabled: true, APIURL: "http://evil.example.com"}, true},
		{"enabled with http loopback is accepted", SyncConfig{Enabled: true, APIURL: "http://localhost:3001"}, false},
		{"enabled with http 127.0.0.1 is accepted", SyncConfig{Enabled: true, APIURL: "http://127.0.0.1:3001"}, false},
		{"enabled with a non-URL is rejected", SyncConfig{Enabled: true, APIURL: "not a url"}, true},
		{"enabled with userinfo is rejected", SyncConfig{Enabled: true, APIURL: "https://user:pass@api.mivia.app"}, true},
		{"enabled with a non-http scheme is rejected", SyncConfig{Enabled: true, APIURL: "file:///etc/passwd"}, true},
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
		Sync: SyncConfig{Enabled: true, APIURL: "http://evil.example.com"},
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
