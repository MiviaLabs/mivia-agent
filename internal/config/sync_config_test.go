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
