package config

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
