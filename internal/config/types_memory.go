package config

// MemoryConfig configures durable agent memory (plan 68).
//
// Org identity is USER-owned: org_id is honored only from the user config
// file (~/.mivia/mivia.toml). A workspace config is repo-controlled and must
// not name the org store its agents write into, so a workspace org_id is
// ignored at load (see resolveMemoryConfig).
type MemoryConfig struct {
	// Enabled controls whether the memory tools are wired. nil (the key
	// omitted) means enabled, so existing configs load unchanged.
	Enabled *bool `toml:"enabled"`
	// StoreBackend is "memory" (ephemeral) or "markdown" (durable).
	StoreBackend string `toml:"store_backend"`
	// OrgID is the org identity for org-scoped memory, honored from the
	// user config file only. Empty means org scope is unavailable.
	OrgID string `toml:"org_id"`
	// MaxEntryBytes caps one rendered entry. Default 8192.
	MaxEntryBytes int `toml:"max_entry_bytes"`
	// MaxEntries caps the row count per store file. Default 500.
	MaxEntries int `toml:"max_entries"`
	// MaxSearchResults caps memory_search results. Default 8.
	MaxSearchResults int `toml:"max_search_results"`
	// IndexRefreshIntervalSeconds bounds the background memory index
	// reconciler's full-scan fallback, and how long a read may skip its own
	// rescan because the reconciler marked the scope fresh. Zero means the
	// default; values above MaxMemoryIndexRefreshIntervalSeconds are a load
	// error. The watcher itself has no interval: events apply as they arrive.
	IndexRefreshIntervalSeconds int `toml:"index_refresh_interval_seconds"`
	// BlockPatterns are regexes; a save whose content matches any of them is
	// refused. Configuration-only, like the privacy redaction patterns.
	BlockPatterns []string `toml:"block_patterns"`
	// InjectCore enables auto-injecting the bounded "core" memory tier into
	// the system prompt at session start (D1, plan 76). Default false: it
	// changes every session's prompt composition, and round-2 review of
	// plan 76 found operators who allowlist the mivia binary in
	// [tools].run_allowlist weaken D1a's promotion gate - shipping this off
	// by default means an operator opts into that exposure per repo,
	// consciously, rather than getting new prompt content on upgrade.
	InjectCore bool `toml:"inject_core"`
}

// IsEnabled reports whether memory is enabled (nil means enabled).
func (m MemoryConfig) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}
