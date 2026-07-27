// Package config loads mivia TOML configuration and resolves provider settings.
package config

// File is the on-disk TOML shape (no secrets).
type File struct {
	Provider     ProviderSection           `toml:"provider"`
	Providers    map[string]ProviderConfig `toml:"providers"`
	Chat         ChatConfig                `toml:"chat"`
	Subagents    SubagentConfig            `toml:"subagents"`
	Privacy      PrivacyConfig             `toml:"privacy"`
	Integrations IntegrationsConfig        `toml:"integrations"`
}

// IntegrationsConfig holds API keys and config for third-party services.
type IntegrationsConfig struct {
	Tavily TavilyConfig `toml:"tavily"`
}

// TavilyConfig configures the Tavily web search integration.
type TavilyConfig struct {
	// APIKeyEnv overrides the env var name (default "TAVILY_API_KEY").
	APIKeyEnv string `toml:"api_key_env"`
	// Disable explicitly disables Tavily even if the env var is set.
	Disable bool `toml:"disable"`
}

// PrivacyConfig controls operator-visible redaction of tool I/O.
// RedactToolArgs defaults to false (show argv/args). Enable via TOML or
// MIVIA_REDACT_TOOL_ARGS for stricter privacy in shared/recorded sessions.
type PrivacyConfig struct {
	RedactToolArgs bool `toml:"redact_tool_args"`
}

// ProviderSection selects the active provider.
type ProviderSection struct {
	Name    string `toml:"name"`
	EnvFile string `toml:"env_file"`
}

// ProviderConfig holds non-secret provider settings.
type ProviderConfig struct {
	Model       string `toml:"model"`
	BaseURL     string `toml:"base_url"`
	APIKeyEnv   string `toml:"api_key_env"`
	HTTPReferer string `toml:"http_referer"`
	XTitle      string `toml:"x_title"`
}

// ChatConfig holds chat session defaults.
type ChatConfig struct {
	SystemPrompt     string   `toml:"system_prompt"`
	MaxContextTokens *int     `toml:"max_context_tokens"`
	Temperature      *float64 `toml:"temperature"`
	MaxTokens        *int     `toml:"max_tokens"`
}

// SubagentConfig holds subagent execution policy.
type SubagentConfig struct {
	MaxWorkers     int    `toml:"max_workers"`
	MaxDepth       int    `toml:"max_depth"`
	MaxFanout      int    `toml:"max_fanout"`
	DefaultTimeout int    `toml:"default_timeout_seconds"`
	DefaultBudget  int    `toml:"default_budget"`
	PartialResults bool   `toml:"partial_results"`
	SystemPrompt   string `toml:"system_prompt"`
	NestedSteps    int    `toml:"nested_steps"`
}

// Resolved is the fully resolved runtime config used by the CLI.
type Resolved struct {
	ConfigPath   string
	EnvFilePath  string
	EnvFileUsed  bool
	ProviderName string
	Model        string
	BaseURL      string
	APIKeyEnv    string
	APIKeySet    bool
	// APIKey is populated only for runtime use; never print it.
	APIKey           string
	HTTPReferer      string
	XTitle           string
	SystemPrompt     string
	MaxContextTokens int
	Temperature      *float64
	MaxTokens        *int
	Subagents        SubagentConfig
	// Privacy is resolved from [privacy] TOML and MIVIA_REDACT_TOOL_ARGS.
	Privacy PrivacyConfig

	// TavilyAPIKey is the Tavily web search API key (set via TAVILY_API_KEY env).
	// When set, the search tool uses Tavily as the primary web search engine.
	TavilyAPIKey string
}
