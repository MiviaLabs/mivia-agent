package config

import (
	"fmt"
	"strings"
)

// SyncConfig controls remote chat session synchronization.
// Defaults are fail-closed: sync is disabled, tool IO and thinking are withheld.
type SyncConfig struct {
	Enabled          bool   `toml:"enabled"`
	IncludeToolIO    bool   `toml:"include_tool_io"`
	IncludeThinking  bool   `toml:"include_thinking"`
	APIURL           string `toml:"api_url"`
	PollWaitSeconds  int    `toml:"poll_wait_seconds"`
	HeartbeatSeconds int    `toml:"heartbeat_seconds"`
	MaxUnflushed     int    `toml:"max_unflushed"`
}

func resolveSyncConfig(cfg SyncConfig) SyncConfig {
	if cfg.PollWaitSeconds <= 0 {
		cfg.PollWaitSeconds = 25
	}
	if cfg.HeartbeatSeconds <= 0 {
		cfg.HeartbeatSeconds = 30
	}
	if cfg.MaxUnflushed <= 0 {
		cfg.MaxUnflushed = 5000
	}
	return cfg
}

// validateSyncConfig gates the transcript-upload endpoint.
//
// api_url decides where every message of every conversation is sent, so it
// gets the same shape internal/miviaauth/client.go applies to the auth
// endpoint: a well-formed absolute https URL, or plain http on a literal
// loopback host so a local API (typically http://localhost:3001) still works.
//
// It deliberately does NOT go through validateBaseURL. That function honours
// MIVIA_ALLOW_INSECURE_HTTP, which exists for local provider mocks; inheriting
// it here would let one environment variable redirect conversation content
// onto a cleartext host that is not loopback.
//
// A disabled sync is not validated at all: an unused key is not a
// misconfiguration, and refusing to start over one would make `enabled =
// false` harder to reach than the state it protects against.
func validateSyncConfig(cfg SyncConfig) error {
	if !cfg.Enabled {
		return nil
	}
	raw := strings.TrimSpace(cfg.APIURL)
	if raw == "" {
		return fmt.Errorf("[sync] api_url is required when sync is enabled")
	}
	if _, err := ValidateHTTPSURL(raw); err == nil {
		return nil
	}
	if IsOllamaLoopback(raw) {
		return nil
	}
	// Fixed literal on purpose: api_url may carry credentials or control
	// characters, and this message reaches logs.
	return fmt.Errorf("[sync] api_url must be an absolute https URL, or http on a loopback host")
}
