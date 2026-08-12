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
	Worktrees    WorktreeConfig            `toml:"worktrees"`
	Tools        ToolsConfig               `toml:"tools"`
	Privacy      PrivacyConfig             `toml:"privacy"`
	Context      ContextConfig             `toml:"context"`
	Integrations IntegrationsConfig        `toml:"integrations"`
	MCP          MCPConfig                 `toml:"mcp"`
	Memory       MemoryConfig              `toml:"memory"`
	Harness      HarnessConfig             `toml:"harness"`
}

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
	// StoreBackend is "memory" (ephemeral, in-process) or "sqlite"
	// (durable, default). Mirrors [subagents] store_backend.
	StoreBackend string `toml:"store_backend"`
	// StorePath is the project memory database file. Empty uses
	// <workspace>/.mivia/memory.db. A repo owner may point it at a tracked
	// path and commit memories with the repository. Relative paths resolve
	// against the workspace root; "~/..." expands to the home directory.
	StorePath string `toml:"store_path"`
	// OrgID is the org identity for org-scoped memory, honored from the
	// user config file only. Empty means org scope is unavailable.
	OrgID string `toml:"org_id"`
	// MaxEntryBytes caps one rendered entry. Default 8192.
	MaxEntryBytes int `toml:"max_entry_bytes"`
	// MaxEntries caps the row count per store file. Default 500.
	MaxEntries int `toml:"max_entries"`
	// MaxSearchResults caps memory_search results. Default 8.
	MaxSearchResults int `toml:"max_search_results"`
	// BlockPatterns are regexes; a save whose content matches any of them is
	// refused. Configuration-only, like the privacy redaction patterns.
	BlockPatterns []string `toml:"block_patterns"`
}

// IsEnabled reports whether memory is enabled (nil means enabled).
func (m MemoryConfig) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

// MCPConfig controls trusted MCP server definitions. A project definition
// replaces a user definition with the same server ID as one complete unit.
type MCPConfig struct {
	Enabled                 bool              `toml:"enabled"`
	StartupTimeoutSeconds   int               `toml:"startup_timeout_seconds"`
	MaxServers              int               `toml:"max_servers"`
	MaxToolsPerServer       int               `toml:"max_tools_per_server"`
	MaxToolSchemaBytes      int               `toml:"max_tool_schema_bytes"`
	MaxToolDescriptionBytes int               `toml:"max_tool_description_bytes"`
	MaxToolResultBytes      int               `toml:"max_tool_result_bytes"`
	Servers                 []MCPServerConfig `toml:"servers"`
}

// MCPServerConfig is one MCP server definition. It stores only environment
// variable names. It never stores a secret value.
type MCPServerConfig struct {
	ID             string            `toml:"id"`
	Transport      string            `toml:"transport"`
	Command        string            `toml:"command"`
	URL            string            `toml:"url"`
	Args           []string          `toml:"args"`
	Env            []string          `toml:"env"`
	Headers        []MCPHeaderConfig `toml:"headers"`
	Global         bool              `toml:"global"`
	TimeoutSeconds int               `toml:"timeout_seconds"`
}

// MCPHeaderConfig maps an HTTP header to the name of its environment value.
type MCPHeaderConfig struct {
	Name     string `toml:"name"`
	ValueEnv string `toml:"value_env"`
}

// ContextConfig is the operator's ceiling on durable context storage.
//
// Every field is bytes-or-count, and EVERY ONE DEFAULTS TO 0 = UNCAPPED. These
// used to be constants compiled into the durable contract, sized far below the
// 200k-1M token windows this product ships against, and because publication is
// one transaction, exceeding one refused the whole turn: the conversation
// stopped persisting the first time the agent read a file, and never recovered
// because an active context only grows. A ceiling here is a deliberate storage
// decision by someone who knows their disk, never a default that destroys work
// the agent already finished.
type ContextConfig struct {
	// MaxSourceEventBytes bounds one projected message's payload.
	MaxSourceEventBytes int `toml:"max_source_event_bytes"`
	// MaxCheckpointBytes bounds a checkpoint's serialized active context.
	MaxCheckpointBytes int `toml:"max_checkpoint_bytes"`
	// MaxCommitEvents bounds how many messages one turn may publish.
	MaxCommitEvents int `toml:"max_commit_events"`
	// MaxCommitEventBytes bounds one turn's aggregate payload bytes.
	MaxCommitEventBytes int `toml:"max_commit_event_bytes"`
	// MaxSessionStateBytes bounds a stored session's serialized messages.
	MaxSessionStateBytes int `toml:"max_session_state_bytes"`
	// MaxExportBytes bounds a context export.
	MaxExportBytes int `toml:"max_export_bytes"`
	// SummaryMetadataBytes bounds the persisted summary envelope. Zero (uncapped
	// by default) means the host imposes no compiled-in ceiling on
	// model-generated summary content.
	SummaryMetadataBytes int `toml:"summary_metadata_bytes"`
	// CheckpointMetadataBytes bounds the summary_metadata column within a
	// checkpoint record. Zero means uncapped.
	CheckpointMetadataBytes int `toml:"checkpoint_metadata_bytes"`
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
	// PromptCache is "auto" (default) or "off". It gates only this host's
	// capture and publication of provider-reported prompt-cache usage
	// accounting - it cannot disable a provider's own automatic caching,
	// which every provider this repo speaks today performs server-side with
	// no request-side control.
	PromptCache string `toml:"prompt_cache"`
}

// ProviderConfig holds non-secret provider settings.
type ProviderConfig struct {
	// Models is the explicit, finite model catalog for this provider.
	Models []ModelSpec `toml:"models,omitempty"`
	// DefaultModel is this provider's default model. When Models is non-empty,
	// it must be a member of the allowlist.
	DefaultModel string `toml:"default_model,omitempty"`
	// LegacyModel is a decode sentinel. The old scalar provider model key is
	// rejected explicitly so it cannot override an explicit catalog entry.
	LegacyModel *string `toml:"model"`
	BaseURL     string  `toml:"base_url"`
	APIKeyEnv   string  `toml:"api_key_env"`
	HTTPReferer string  `toml:"http_referer"`
	XTitle      string  `toml:"x_title"`
}

// ChatConfig holds chat session defaults.
type ChatConfig struct {
	SystemPrompt    string `toml:"system_prompt"`
	MaxPromptTokens *int   `toml:"max_prompt_tokens"`
	// MaxContextTokens is retained only as a decode sentinel so the removed
	// setting cannot silently change the prompt safety budget.
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
	MaxWorkers int `toml:"max_workers"`
	// MaxDepth caps the dependency depth of one orchestrated task graph
	// (spawn_agent depends_on chains). 0 means unlimited (default); a
	// positive value caps the depth.
	MaxDepth int `toml:"max_depth"`
	// MaxFanout caps the number of tasks admitted in one orchestration.
	// 0 means unlimited (default); a positive value caps the count.
	MaxFanout      int `toml:"max_fanout"`
	DefaultTimeout int `toml:"default_timeout_seconds"`
	// DefaultRequestTimeoutSec is the per-LLM-request timeout for subagents
	// (seconds). When 0, requestTimeout() falls back to the effective
	// orchestration timeout (DefaultOrchestrationTimeoutSec = 12h).
	DefaultRequestTimeoutSec int    `toml:"default_request_timeout_seconds"`
	DefaultBudget            int    `toml:"default_budget"`
	SystemPrompt             string `toml:"system_prompt"`
	NestedSteps              int    `toml:"nested_steps"`

	// StoreBackend selects the ledger storage backend: "memory" (default) or "sqlite".
	StoreBackend string `toml:"store_backend"`

	// StorePath is the configured SQLite file path. Chat uses this path for
	// sessions, context, worktree routes, and orchestration.
	StorePath string `toml:"store_path"`

	// HandleRetentionSeconds controls how long completed orchestration run
	// handles remain accessible via inspect_agents/join_run/cancel_run
	// before automatic eviction. Default: 600 (10 minutes). 0 = no retention.
	HandleRetentionSeconds int `toml:"handle_retention_seconds"`

	// MaxAuditRounds controls the maximum number of ADLC Step 5 bug audit
	// rounds. When 0 (default), rounds are unlimited. Set to a positive
	// value to cap.
	MaxAuditRounds int `toml:"max_audit_rounds"`

	// InlineOutputBytes is the per-task output size threshold (bytes). Task
	// results whose output body is at or below this threshold are inlined in
	// the model-visible result envelope (the "output" field). Results above
	// this threshold emit only "output_ref", "output_bytes", and a bounded
	// "synopsis"; the parent fetches the full body via ledger_read.
	// Default: 4096. An explicit 0 means "always use refs" (never inline) and
	// is preserved through resolution via inlineOutputBytesSet; an absent key
	// falls back to the 4096 default. Errors follow the same rule with
	// "error"/"error_ref".
	InlineOutputBytes int `toml:"inline_output_bytes"`

	// inlineOutputBytesSet records whether [subagents] inline_output_bytes was
	// present in the config file, so an explicit 0 ("always use refs") survives
	// the defaulting in resolveSubagentConfig. No toml tag: go-toml/v2 ignores
	// unexported fields, so the flag is set only by loadFile's raw-byte probe.
	inlineOutputBytesSet bool

	// SchemaRetryMax is how many corrective re-entries a multi-step child may
	// take after an invalid schema-validated reply (plan tools/02). Default 2.
	// The initial attempt is separate: retry_max=2 allows two corrective turns.
	// Clamped at load: <= 0 means the default 2; a positive value above
	// MaxSchemaRetryMax is clamped to it.
	SchemaRetryMax int `toml:"schema_retry_max"`

	// Messaging configures typed agent-to-agent messaging (plan 53). Nested
	// under [subagents.messaging]. Always enabled (product decision 2026-08-03).
	Messaging MessagingConfig `toml:"messaging"`

	// TaskRetry configures automatic retry/backoff for a dispatched task whose
	// failure is classified transient (provider.IsTransient - network blip,
	// 429, 5xx, timeout) or that timed out. Nested under [subagents.retry].
	// Distinct from SchemaRetryMax above, which governs corrective re-entries
	// after an invalid schema-validated reply within one task, not whole-task
	// retry. All-zero (the default) disables retry entirely, identical to
	// today's behavior: a deployment must opt in. See task_retry_config.go
	// for the TaskRetryConfig type.
	TaskRetry TaskRetryConfig `toml:"retry"`
}

// WorktreeConfig controls worktree branch settings.
type WorktreeConfig struct {
	// BranchPrefix is the prefix for branches that mivia creates for worktrees.
	BranchPrefix string `toml:"branch_prefix"`
}

// MessagingConfig is the [subagents.messaging] surface for typed, budgeted
// agent messages. Messaging is always on; Enabled is accepted in TOML for
// forward compatibility but ignored (IsEnabled always returns true).
type MessagingConfig struct {
	// Enabled is ignored: messaging is always enabled. Retained so older
	// configs with enabled=true|false still parse without error.
	Enabled *bool `toml:"enabled"`
	// MaxBodyBytes is the per-message inline body budget. Default 2048.
	MaxBodyBytes int `toml:"max_body_bytes"`
	// MaxMessagesPerTask is the child upstream send quota per attempt. Default 32.
	MaxMessagesPerTask int `toml:"max_messages_per_task"`
	// MailboxCapacity is parent→child mailbox depth (phase 03). Default 32.
	MailboxCapacity int `toml:"mailbox_capacity"`
	// MaxPendingQuestions is RESERVED and a no-op: the effective value is always
	// 1. Exactly one park per task is structurally enforced by the question
	// registry (one pendingQuestion per runID/taskID key) plus the awaiting_input
	// single-bit ledger status; N>1 is unsupported. The field still parses from
	// TOML (and the config resolver still fills the default of 1) so existing
	// configs load unchanged, but nothing reads it for behavior.
	MaxPendingQuestions int `toml:"max_pending_questions"`
	// SteerWatchdogSeconds: nil = default (300s); explicit 0 = disabled
	// (unbounded); positive = seconds.
	SteerWatchdogSeconds *int `toml:"steer_watchdog_seconds"`
	// Routing is parent-side Ask referral policy (plan 53.04). Always active.
	Routing MessagingRoutingConfig `toml:"routing"`
}

// MessagingRoutingConfig is [subagents.messaging.routing] for peer referral.
// mode "policy" is implemented; "parent" is declared but unimplemented.
type MessagingRoutingConfig struct {
	// Mode is "policy" (default) or "parent" (unimplemented).
	Mode string `toml:"mode"`
	// MaxAsksPerTask bounds UNANSWERED asks posted by one task. Default 4.
	// Semantics: the slot is released when an ask is answered or sealed.
	MaxAsksPerTask int `toml:"max_asks_per_task"`
	// MaxReferralDepth is max hops in an ask chain (A→B→C = 2). Default 2.
	MaxReferralDepth int `toml:"max_referral_depth"`
	// Allow is "from_role->to_role" pairs. Empty = any live same-run role;
	// referral-as-spawn always requires an explicit pair.
	Allow []string `toml:"allow"`
	// MaxReferralSpawnsPerRun caps referral-as-spawn. Default 4.
	MaxReferralSpawnsPerRun int `toml:"max_referral_spawns_per_run"`
}

// IsEnabled always returns true. Messaging cannot be disabled (product
// decision 2026-08-03); the TOML enabled field is ignored if present.
func (m MessagingConfig) IsEnabled() bool {
	return true
}

// SteerWatchdogSecondsResolved returns the effective steer watchdog interval
// in seconds: nil → the default 300, an explicit 0 → disabled (unbounded),
// otherwise the configured value. Single source of truth for the CLI handler
// construction sites (plan 54 §4.5); the config-layer resolver
// (resolveMessagingConfig) fills nil the same way, so this is idempotent on
// both resolved and raw configs.
func (m MessagingConfig) SteerWatchdogSecondsResolved() int {
	if m.SteerWatchdogSeconds == nil {
		return *DefaultMessagingConfig.SteerWatchdogSeconds
	}
	return *m.SteerWatchdogSeconds
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
	// Models is retained as a compatibility projection of ModelProfiles.
	Models []string
	// ModelProfiles is the active provider's copied model catalog.
	ModelProfiles []ModelSpec
	// ProviderRuntimes contains resolved backend material for provider.NewForProvider.
	ProviderRuntimes map[string]ProviderRuntime
	modelCatalog     []ProviderModelGroup
	BaseURL          string
	APIKeyEnv        string
	APIKeySet        bool
	// APIKey is populated only for runtime use; never print it.
	APIKey          string
	HTTPReferer     string
	XTitle          string
	SystemPrompt    string
	MaxPromptTokens *int
	// MaxContextTokens is retained as a compatibility projection of the
	// selected model's effective prompt budget.
	MaxContextTokens int
	Temperature      *float64
	MaxTokens        *int
	Subagents        SubagentConfig
	Worktrees        WorktreeConfig
	StoreBackend     string
	StorePath        string
	// StorePathSet reports whether [subagents].store_path was set in the
	// selected configuration. It lets repository storage resolve its default
	// from the repository root instead of the current worktree.
	StorePathSet bool
	// Privacy is resolved from [privacy] TOML and MIVIA_REDACT_TOOL_ARGS.
	Privacy PrivacyConfig
	// Context is the operator's durable storage ceilings, uncapped by default.
	Context ContextConfig
	// Tools is the resolved tool execution policy.
	Tools ToolsConfig
	// MCP is the resolved MCP server configuration.
	MCP MCPConfig
	// MCPWarnings are scrubbed operator diagnostics for the MCP configuration.
	MCPWarnings []string
	// Memory is the resolved [memory] configuration.
	Memory MemoryConfig
	// Harness is the resolved [harness] configuration.
	Harness HarnessConfig

	// TavilyAPIKey is the Tavily web search API key (set via TAVILY_API_KEY env).
	// When set, the search tool uses Tavily as the primary web search engine.
	TavilyAPIKey string

	// PromptCache is the resolved "auto" or "off" policy for capturing
	// provider-reported prompt-cache usage accounting. Always one of those
	// two values after Load - see ProviderSection.PromptCache.
	PromptCache string
}

// ProviderRuntime contains resolved provider construction settings. It is not
// returned by ModelCatalog and must never be rendered or sent to model-facing
// tools. APIKey is only consumed by the provider factory.
type ProviderRuntime struct {
	ProviderName string
	BaseURL      string
	APIKeyEnv    string
	APIKeySet    bool
	APIKey       string
	HTTPReferer  string
	XTitle       string
	Models       []ModelSpec
}

// ProviderModelGroup is a secret-free provider group for the model picker.
type ProviderModelGroup struct {
	Provider       string
	Models         []ModelSpec
	Active         bool
	Selectable     bool
	DisabledReason string
}

// AllowsModel reports whether name may be selected under the resolved policy.
func (r *Resolved) AllowsModel(name string) bool {
	name, err := NormalizeModelName(name)
	if err != nil {
		return false
	}
	if len(r.ModelProfiles) > 0 {
		for _, profile := range r.ModelProfiles {
			if profile.Name == name {
				return true
			}
		}
		return false
	}
	return len(r.Models) == 0 || slices.Contains(r.Models, name)
}

// ModelChoices renders the selectable set for usage and error messages.
func (r *Resolved) ModelChoices() string {
	if len(r.Models) > 0 {
		return strings.Join(r.Models, ", ")
	}
	choices := make([]string, 0, len(r.ModelProfiles))
	for _, profile := range r.ModelProfiles {
		choices = append(choices, profile.Name)
	}
	return strings.Join(choices, ", ")
}

// ModelChoicesFor renders the selectable catalog for one provider.
func (r *Resolved) ModelChoicesFor(providerName string) string {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	for _, group := range r.ModelCatalog() {
		if group.Provider != providerName || !group.Selectable {
			continue
		}
		choices := make([]string, 0, len(group.Models))
		for _, profile := range group.Models {
			choices = append(choices, profile.Name)
		}
		return strings.Join(choices, ", ")
	}
	if providerName == r.ProviderName {
		return r.ModelChoices()
	}
	return ""
}

// ModelCatalog returns a deep copy of the secret-free provider catalog.
// ReasoningEfforts is cloned per model because cloning the []ModelSpec alone
// would leave every caller sharing one backing array with the stored catalog.
func (r *Resolved) ModelCatalog() []ProviderModelGroup {
	if r == nil {
		return nil
	}
	out := make([]ProviderModelGroup, len(r.modelCatalog))
	for i, group := range r.modelCatalog {
		out[i] = group
		out[i].Models = slices.Clone(group.Models)
		for j, spec := range out[i].Models {
			out[i].Models[j].ReasoningEfforts = slices.Clone(spec.ReasoningEfforts)
		}
	}
	return out
}
