// Package config loads mivia TOML configuration and resolves provider settings.
package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/pelletier/go-toml/v2/unstable"
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
	Context      ContextConfig             `toml:"context"`
	Integrations IntegrationsConfig        `toml:"integrations"`
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

// ModelSpec is one explicitly configured provider model and its physical
// context capacity. The name is provider-qualified by its containing group.
type ModelSpec struct {
	Name                string `toml:"name"`
	ContextWindowTokens int    `toml:"context_window_tokens"`
	MaxOutputTokens     int    `toml:"max_output_tokens,omitempty"`
}

// UnmarshalTOML enforces the narrow model object shape. A scalar model array
// is rejected instead of being silently treated as an empty catalog.
func (m *ModelSpec) UnmarshalTOML(value *unstable.Node) error {
	if value == nil || (value.Kind != unstable.InlineTable && value.Kind != unstable.Table) {
		return fmt.Errorf("model must be an object")
	}
	var name string
	var context int
	maxOutput := 0
	for child := value.Child(); child != nil; child = child.Next() {
		key := child.Key()
		keyNode := key.Node()
		if keyNode == nil {
			return fmt.Errorf("invalid model object")
		}
		valueNode := child.Value()
		switch string(keyNode.Data) {
		case "name":
			if valueNode.Kind != unstable.String {
				return fmt.Errorf("invalid model object")
			}
			name = string(valueNode.Data)
		case "context_window_tokens":
			if valueNode.Kind != unstable.Integer {
				return fmt.Errorf("invalid model object")
			}
			parsed, err := strconv.Atoi(string(valueNode.Data))
			if err != nil {
				return fmt.Errorf("invalid model object")
			}
			context = parsed
		case "max_output_tokens":
			if valueNode.Kind != unstable.Integer {
				return fmt.Errorf("invalid model object")
			}
			parsed, err := strconv.Atoi(string(valueNode.Data))
			if err != nil {
				return fmt.Errorf("invalid model object")
			}
			maxOutput = parsed
		default:
			return fmt.Errorf("invalid model object")
		}
	}
	m.Name = name
	m.ContextWindowTokens = context
	m.MaxOutputTokens = maxOutput
	return nil
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
	MaxWorkers     int `toml:"max_workers"`
	MaxDepth       int `toml:"max_depth"`
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

	// StorePath is the SQLite file path (only used when StoreBackend == "sqlite").
	// If empty, a platform-specific default is resolved.
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
	// Default: 4096. 0 means "always use refs" (never inline).
	// Errors follow the same rule with "error"/"error_ref".
	InlineOutputBytes int `toml:"inline_output_bytes"`

	// SchemaRetryMax is how many corrective re-entries a multi-step child may
	// take after an invalid schema-validated reply (plan tools/02). Default 2.
	// The initial attempt is separate: retry_max=2 allows two corrective turns.
	SchemaRetryMax int `toml:"schema_retry_max"`

	// Messaging configures typed agent-to-agent messaging (plan 53). Nested
	// under [subagents.messaging]. Always enabled (product decision 2026-08-03).
	Messaging MessagingConfig `toml:"messaging"`
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
	// TOML (and load.go still fills the default of 1) so existing configs load
	// unchanged, but nothing reads it for behavior.
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
	// MaxAsksPerTask bounds asks posted by one task. Default 4.
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
	StoreBackend     string
	StorePath        string
	// Privacy is resolved from [privacy] TOML and MIVIA_REDACT_TOOL_ARGS.
	Privacy PrivacyConfig
	// Context is the operator's durable storage ceilings, uncapped by default.
	Context ContextConfig
	// Tools is the resolved tool execution policy.
	Tools ToolsConfig

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
func (r *Resolved) ModelCatalog() []ProviderModelGroup {
	if r == nil {
		return nil
	}
	out := make([]ProviderModelGroup, len(r.modelCatalog))
	for i, group := range r.modelCatalog {
		out[i] = group
		out[i].Models = slices.Clone(group.Models)
	}
	return out
}
