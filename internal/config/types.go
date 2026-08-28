// Package config loads mivia TOML configuration and resolves provider settings.
package config

import (
	"slices"
	"strings"
	"time"

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
	Approvals    ApprovalsConfig           `toml:"approvals"`
	TUI          TUIConfig                 `toml:"tui"`
	Workflows    WorkflowsConfig           `toml:"workflows"`
	// Verifiers is populated by LoadWorkspaceVerifiers from the WORKSPACE'S
	// own .mivia/mivia.toml only (loadFile), never by the tolerant struct
	// decode and never from a user-level base layer: a verifier table with an
	// unknown key must fail the load, and the profiles that judge a project's
	// gates must come from that project's file alone.
	Verifiers map[string]VerifierProfile `toml:"-"`
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
	// Summary is the [context.summary] policy sub-table. Unlike the numeric
	// ceilings above, it is behavior policy, not a storage bound.
	Summary ContextSummaryConfig `toml:"summary"`
}

// ContextSummaryConfig is the operator switch for LLM-backed compaction
// summaries. It is ENABLED by default: compaction drops messages permanently,
// so the summary is the only record of what was removed, and a workspace that
// configures nothing should not silently lose that record. Opting out is
// explicit.
type ContextSummaryConfig struct {
	// Enabled turns on the bounded provider call that summarizes what
	// compaction dropped. Nil means unset, which resolves to true - the
	// pointer exists precisely so an explicit `enabled = false` is
	// distinguishable from an absent key, which a plain bool cannot express.
	// The call still requires a resolved provider endpoint; without one the
	// summary stays off and summaryDisabledReason names why.
	Enabled *bool `toml:"enabled"`

	// Provider and Model override the binding the summary call runs on,
	// allowing summaries to use a cheaper model than the session binding.
	// Both keys must be set together, and Provider must be a known provider
	// declared under [providers] - the model is not required to appear in
	// that provider's declared models (agent-file precedent: a bad model
	// degrades at call time, and summary failures are intentionally soft).
	// Absent means the summary uses the session binding captured at setup.
	Provider *string `toml:"provider"`
	Model    *string `toml:"model"`
}

// SummaryEnabled reports the resolved switch: absent means on. Read this
// rather than the pointer so the opt-out default lives in one place.
func (c ContextSummaryConfig) SummaryEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// WireStreamResolved reports the resolved wire-stream switch: absent means
// on. Read this rather than the pointer so the opt-out default lives in one
// place.
func (c SubagentConfig) WireStreamResolved() bool {
	return c.WireStream == nil || *c.WireStream
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
	// PromptCache is "auto" (default) or "off". "auto" enables capture of
	// provider-reported prompt-cache usage accounting and, on providers that
	// honor explicit markers (openrouter), emission of cache_control markers
	// on the stable prefix. "off" disables both. It cannot disable a
	// provider's own automatic server-side caching.
	PromptCache string `toml:"prompt_cache"`
	// StreamIdleTimeoutSeconds bounds the gap between successive bytes on any
	// provider read (streaming or non-streaming), once the first byte has
	// arrived. Unset (nil) resolves to DefaultStreamIdleTimeoutSeconds
	// (100s). This is a process-wide setting, not per-provider: mivia runs
	// one active provider configuration per process.
	StreamIdleTimeoutSeconds *int `toml:"stream_idle_timeout_seconds"`
	// StreamFirstByteTimeoutSeconds bounds the wait for the first byte of a
	// provider read, from request-issued. Unset (nil) resolves to
	// DefaultStreamFirstByteTimeoutSeconds (240s).
	StreamFirstByteTimeoutSeconds *int `toml:"stream_first_byte_timeout_seconds"`
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
	// ShowIterationNotices controls whether per-step/iteration notices (e.g. "iteration 1")
	// are emitted in the TUI chat. Default: false (disabled).
	ShowIterationNotices *bool `toml:"show_iteration_notices"`
	// ShowPromptCacheNotices controls whether prompt cache hit/usage notices
	// are emitted in the TUI chat. Default: false (disabled).
	ShowPromptCacheNotices *bool `toml:"show_prompt_cache_notices"`
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
	// (seconds). When 0, requestTimeout() uses DefaultSubagentRequestTimeoutSec
	// (1800s, 30 minutes) as the per-request context deadline. The 15-minute
	// http.Client wall stays the hard per-attempt bound; the 12-hour
	// orchestration default no longer feeds individual subagent requests.
	DefaultRequestTimeoutSec int `toml:"default_request_timeout_seconds"`
	// DefaultTotalTimeoutSec is the whole-subagent wall-clock budget
	// (seconds). 0 = unset = DefaultSubagentTotalTimeoutSec (1200s, 20
	// minutes). Negative = off: a direct spawn with no per-task timeout then
	// has no handler-level bound at all, and workflow or panel steps whose
	// own timeout is unset stay bounded only by workflow policy - this is an
	// explicit operator opt-out of the last-resort termination guarantee. A
	// positive value is the budget itself; a tighter per-task timeout from
	// the caller still wins.
	DefaultTotalTimeoutSec int `toml:"default_total_timeout_seconds"`
	// WireStream opts nested subagent calls into the wire-stream transport:
	// the request goes to the provider with stream:true while the call keeps
	// its non-stream contract - the full answer is assembled before it comes
	// back. Nil means unset, which resolves to true - the pointer exists so
	// an explicit `wire_stream = false` is distinguishable from an absent
	// key, which a plain bool cannot express. False opts out and keeps every
	// nested call on the plain non-stream endpoint.
	WireStream    *bool  `toml:"wire_stream"`
	DefaultBudget int    `toml:"default_budget"`
	SystemPrompt  string `toml:"system_prompt"`
	NestedSteps   int    `toml:"nested_steps"`

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

	// SpawnStaggerMs staggers the start of each task after the first within
	// one dispatch batch by this many milliseconds, so concurrent workers do
	// not fire their first provider call on the same instant (the step-1
	// thundering-herd hang behind overloaded local proxies). Default: 150.
	// An explicit 0 disables staggering and is preserved through resolution
	// via spawnStaggerMsSet, mirroring inline_output_bytes; an absent key
	// falls back to the default. Values above 1000 are clamped to 1000.
	SpawnStaggerMs int `toml:"spawn_stagger_ms"`

	// spawnStaggerMsSet records whether [subagents] spawn_stagger_ms was
	// present in the config file, so an explicit 0 (disabled) survives the
	// defaulting in resolveSubagentConfig. No toml tag: go-toml/v2 ignores
	// unexported fields, so the flag is set only by loadFile's raw-byte probe.
	spawnStaggerMsSet bool

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

// TUIConfig controls terminal user interface preferences.
type TUIConfig struct {
	Theme         string `toml:"theme"`
	Mouse         *bool  `toml:"mouse"`
	ShowReasoning *bool  `toml:"show_reasoning"`
	ScrollLines   *int   `toml:"scroll_lines"`
	ScreenReader  *bool  `toml:"screen_reader"`
	ReducedMotion *bool  `toml:"reduced_motion"`
}

// WorkflowsConfig holds workflow-engine defaults.
type WorkflowsConfig struct {
	Panels WorkflowPanelLimits `toml:"panels"`
}

// WorkflowPanelLimits overrides the compiled defaults every agent_panel
// step's member and synthesis children run under
// (internal/workflows/controller.DefaultPanelLimits, applied in
// buildPanelAttempt/buildPanelSynthesisWork). A nil field keeps the
// compiled default; this mirrors the [chat] max_steps *int
// nil-means-default pattern (ChatConfig.MaxSteps above), not a new
// convention.
type WorkflowPanelLimits struct {
	MemberMaxOutputPerCall    *int `toml:"member_max_output_per_call"`
	MemberMaxToolCalls        *int `toml:"member_max_tool_calls"`
	SynthesisMaxOutputPerCall *int `toml:"synthesis_max_output_per_call"`
	SynthesisMaxToolCalls     *int `toml:"synthesis_max_tool_calls"`
	// MemberDeadlineDefaultSeconds overrides the wall-clock default a
	// panel member attempt gets when the workflow declares no run
	// deadline (max_duration_seconds = 0). Seconds, matching
	// definition.Limits.MaxDurationSeconds' unit.
	MemberDeadlineDefaultSeconds *int `toml:"member_deadline_default_seconds"`
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
	MaxContextTokens       int
	Temperature            *float64
	MaxTokens              *int
	ShowIterationNotices   bool
	ShowPromptCacheNotices bool
	Subagents              SubagentConfig
	Worktrees              WorktreeConfig
	StoreBackend           string
	StorePath              string
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
	// Approvals is the resolved [approvals] configuration.
	Approvals ApprovalsConfig
	// TUI is the resolved [tui] configuration.
	TUI TUIConfig
	// Workflows is the resolved [workflows] configuration.
	Workflows WorkflowsConfig
	// Verifiers is the workspace-declared verifier profile set from the
	// [verifiers.<name>] tables. The host ships no built-in profiles.
	Verifiers map[string]VerifierProfile

	// TavilyAPIKey is the Tavily web search API key (set via TAVILY_API_KEY env).
	// When set, the search tool uses Tavily as the primary web search engine.
	TavilyAPIKey string
	// StreamIdleTimeout is the resolved [provider] stream_idle_timeout_seconds,
	// defaulted when unset. See ProviderSection.StreamIdleTimeoutSeconds.
	StreamIdleTimeout time.Duration
	// StreamFirstByteTimeout is the resolved [provider]
	// stream_first_byte_timeout_seconds, defaulted when unset. See
	// ProviderSection.StreamFirstByteTimeoutSeconds.
	StreamFirstByteTimeout time.Duration

	// PromptCache is the resolved "auto" or "off" policy for prompt-cache
	// usage capture and explicit cache_control markers. Always one of those
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
	DefaultModel   string
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

// OtherProvidersWithModel returns the provider names, in ModelCatalog order,
// of every Selectable provider (other than exclude) whose catalog contains a
// model named exactly name. Matching is exact (case-sensitive, trimmed),
// matching AllowsModel's and the model picker's own comparison - model names
// are provider-declared identifiers, not free text, so this does not
// normalize case the way a user-facing search would.
//
// This is the single cross-provider model lookup for BOTH the classic REPL
// (internal/clichat) and the new TUI (internal/uiadapter): those two
// packages do not import each other, so the shared logic lives here, one
// level below both, rather than being duplicated (or worse, silently
// diverging - see the pre-existing bug this fixes: internal/uiadapter's
// resolveProviderAndModel used to return the FIRST provider whose catalog
// happened to contain the name, in catalog order, with no check for a second
// match - a silent, order-dependent provider switch on any name collision).
// Only Selectable providers are considered: a provider with no API key set
// is not a switch that would actually work, so it must not appear in a
// "found under provider X" hint that tells the user to try it.
func (r *Resolved) OtherProvidersWithModel(exclude, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	exclude = strings.ToLower(strings.TrimSpace(exclude))
	var found []string
	for _, group := range r.ModelCatalog() {
		if !group.Selectable || strings.ToLower(group.Provider) == exclude {
			continue
		}
		for _, m := range group.Models {
			if m.Name == name {
				found = append(found, group.Provider)
				break
			}
		}
	}
	return found
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

// SetModelCatalogForTest sets the model catalog on Resolved for testing purposes.
func (r *Resolved) SetModelCatalogForTest(catalog []ProviderModelGroup) {
	if r != nil {
		r.modelCatalog = catalog
	}
}
