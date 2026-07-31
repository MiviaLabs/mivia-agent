// Package config loads mivia TOML configuration and resolves provider settings.
package config

import (
	"slices"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// File is the on-disk TOML shape (no secrets).
type File struct {
	EnvFile      string                    `toml:"env_file"`
	Provider     ProviderSection           `toml:"provider"`
	Providers    map[string]ProviderConfig `toml:"providers"`
	Chat         ChatConfig                `toml:"chat"`
	Subagents    SubagentConfig            `toml:"subagents"`
	Tools        ToolsConfig               `toml:"tools"`
	Privacy      PrivacyConfig             `toml:"privacy"`
	Integrations IntegrationsConfig        `toml:"integrations"`
}

// ToolsConfig configures tool execution policies.
type ToolsConfig struct {
	// RunAllowlist extends the built-in default allowlist (union).
	RunAllowlist []string `toml:"run_allowlist"`
	// RunAllowlistOnly replaces the built-in default allowlist entirely.
	RunAllowlistOnly []string `toml:"run_allowlist_only"`
	// RunBlocklist removes programs from the resolved allowlist (takes precedence).
	RunBlocklist []string `toml:"run_blocklist"`
	// DisableTools removes built-in tools by name.
	DisableTools []string `toml:"disable_tools"`
	// EnvAllowlist extends the built-in default env var allowlist (union).
	// Entries ending in "*" are treated as prefix rules (e.g. "GIT_*" allows all GIT_ vars).
	EnvAllowlist []string `toml:"env_allowlist"`
	// EnvAllowlistOnly replaces the built-in default env var allowlist entirely.
	// Entries ending in "*" are treated as prefix rules (e.g. "GIT_*" allows all GIT_ vars).
	EnvAllowlistOnly []string `toml:"env_allowlist_only"`
	// EnvBlocklist removes vars from the resolved env allowlist (takes precedence).
	// Entries ending in "*" are treated as prefix rules (e.g. "GIT_*" blocks all GIT_ vars).
	EnvBlocklist []string `toml:"env_blocklist"`
	// EnvAllowKeywordBlocklist drops variables whose name contains any of these
	// substrings even when a prefix rule admitted them. Exact-name entries in
	// EnvAllowlist are unaffected, so a build needing one names it explicitly.
	EnvAllowKeywordBlocklist []string `toml:"env_allow_keyword_blocklist"`
	// RunTimeoutSec is the default timeout for run_command (seconds).
	RunTimeoutSec int `toml:"run_timeout_seconds"`
	// MaxReadBytes caps read_file output (bytes).
	MaxReadBytes int `toml:"max_read_bytes"`
	// MaxWriteKB caps write_file content (KiB).
	MaxWriteKB int `toml:"max_write_kb"`
	// MaxOutputBytes caps run_command output (bytes).
	MaxOutputBytes int `toml:"max_output_bytes"`
	// MaxListDirEntries caps list_dir output.
	MaxListDirEntries int `toml:"max_list_dir_entries"`
	// MaxToolResultBytes caps each tool result stored in agent-loop history
	// (bytes), applied identically to the interactive session loop and nested
	// sub-agent loops. 0 (the default) means uncapped: per-tool budgets are
	// the bound. Positive values below 1024 are rejected at load.
	MaxToolResultBytes int `toml:"max_tool_result_bytes"`
	// MaxTavilyResponseBytes bounds the bytes read from a Tavily API response
	// body — the `search` tool's Tavily path and `extract`. It is NOT a
	// truncation cap: the tools never cut content. The bound exists so their
	// maximum output is a finite, declarable number, which the dispatcher's
	// output backstop is derived from; a response over the bound is refused
	// with an explicit error naming this key. 0 or negative resolves to the
	// built-in default (never "unlimited" — an unlimited response could not be
	// declared, and undeclared output is what the backstop destroys). Values
	// outside [1024, 64 MiB] are rejected at load.
	MaxTavilyResponseBytes int `toml:"max_tavily_response_bytes"`
	// RedactToolArgs hides argv from operator-visible output.
	RedactToolArgs bool `toml:"redact_tool_args"`
	// SecretPathPatterns replaces the hard-coded secret path blocklist.
	SecretPathPatterns []string `toml:"secret_path_patterns,omitempty"`
	// SecretPathExceptions adds exceptions to the secret path blocklist.
	SecretPathExceptions []string `toml:"secret_path_exceptions,omitempty"`
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
	// RedactionPatterns are regexes applied to operator-visible text (tool
	// previews, event bodies, audit metadata). Nothing is compiled in: unset
	// means no text is redacted anywhere. An invalid pattern is a load error.
	RedactionPatterns []string `toml:"redaction_patterns"`
	// RedactionKeyNames are JSON object keys whose values are elided wholesale
	// (case-insensitive substring match). Unset means no key-based redaction.
	RedactionKeyNames []string `toml:"redaction_key_names"`
	// RedactionPlaceholder replaces each match. Defaults to "[redacted]".
	RedactionPlaceholder string `toml:"redaction_placeholder"`
}

// ProviderSection selects the active provider.
type ProviderSection struct {
	Name string `toml:"name"`
}

// ProviderConfig holds non-secret provider settings.
type ProviderConfig struct {
	// Models is the allowlist of models this provider may use. Empty means
	// unrestricted. The first entry is the default unless DefaultModel names
	// another member.
	Models []string `toml:"models,omitempty"`
	// DefaultModel is this provider's default model. When Models is non-empty,
	// it must be a member of the allowlist.
	DefaultModel string `toml:"default_model,omitempty"`
	BaseURL      string `toml:"base_url"`
	APIKeyEnv    string `toml:"api_key_env"`
	HTTPReferer  string `toml:"http_referer"`
	XTitle       string `toml:"x_title"`
}

// ChatConfig holds chat session defaults.
type ChatConfig struct {
	SystemPrompt     string   `toml:"system_prompt"`
	MaxContextTokens *int     `toml:"max_context_tokens"`
	Temperature      *float64 `toml:"temperature"`
	MaxTokens        *int     `toml:"max_tokens"`
	// MaxSteps bounds one interactive turn's agent loop. Unset uses the
	// built-in default; 0 means unlimited, which lets a model stuck emitting
	// tool calls run until the user interrupts it. /steps overrides per session.
	MaxSteps *int `toml:"max_steps"`
}

// SubagentConfig holds subagent execution policy and storage configuration.
type SubagentConfig struct {
	MaxWorkers     int    `toml:"max_workers"`
	MaxDepth       int    `toml:"max_depth"`
	MaxFanout      int    `toml:"max_fanout"`
	DefaultTimeout int    `toml:"default_timeout_seconds"`
	DefaultBudget  int    `toml:"default_budget"`
	SystemPrompt   string `toml:"system_prompt"`
	NestedSteps    int    `toml:"nested_steps"`

	// StoreBackend selects the ledger storage backend: "memory" (default) or "sqlite".
	StoreBackend string `toml:"store_backend"`

	// StorePath is the SQLite file path (only used when StoreBackend == "sqlite").
	// If empty, a platform-specific default is resolved.
	StorePath string `toml:"store_path"`

	// HandleRetentionSeconds controls how long completed orchestration run
	// handles remain accessible via inspect_agents/join_run/cancel_run
	// before automatic eviction. Default: 600 (10 minutes). 0 = no retention.
	HandleRetentionSeconds int `toml:"handle_retention_seconds"`

	// MaxAuditRounds controls the maximum number of ADLC Step 5 bug audit
	// rounds. When 0 (default), defaults to 5. Set to -1 for unlimited rounds.
	MaxAuditRounds int `toml:"max_audit_rounds"`
}

// Resolved is the fully resolved runtime config used by the CLI.
type Resolved struct {
	// RedactionPolicy is compiled during Load so an invalid pattern fails at
	// startup. Nil means the workspace configured none, which redacts nothing.
	RedactionPolicy *redact.Policy
	// MaxSteps is nil when unconfigured, so the chat default applies. A
	// configured 0 is meaningful (unlimited) and must not be confused with it.
	MaxSteps     *int
	ConfigPath   string
	EnvFilePath  string
	EnvFileUsed  bool
	ProviderName string
	Model        string
	// Models is the active provider's allowlist. Nil means unrestricted.
	Models    []string
	BaseURL   string
	APIKeyEnv string
	APIKeySet bool
	// APIKey is populated only for runtime use; never print it.
	APIKey           string
	HTTPReferer      string
	XTitle           string
	SystemPrompt     string
	MaxContextTokens int
	Temperature      *float64
	MaxTokens        *int
	Subagents        SubagentConfig
	StoreBackend     string
	StorePath        string
	// Privacy is resolved from [privacy] TOML and MIVIA_REDACT_TOOL_ARGS.
	Privacy PrivacyConfig
	// Tools is the resolved tool execution policy.
	Tools ToolsConfig

	// TavilyAPIKey is the Tavily web search API key (set via TAVILY_API_KEY env).
	// When set, the search tool uses Tavily as the primary web search engine.
	TavilyAPIKey string
}

// AllowsModel reports whether name may be selected under the resolved policy.
func (r *Resolved) AllowsModel(name string) bool {
	name, err := NormalizeModelName(name)
	if err != nil {
		return false
	}
	return len(r.Models) == 0 || slices.Contains(r.Models, name)
}

// ModelChoices renders the selectable set for usage and error messages.
func (r *Resolved) ModelChoices() string {
	return strings.Join(r.Models, ", ")
}
