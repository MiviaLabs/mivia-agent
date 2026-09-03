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
	Sync         SyncConfig                `toml:"sync"`
	// Verifiers is populated by LoadWorkspaceVerifiers from the WORKSPACE'S
	// own .mivia/mivia.toml only (loadFile), never by the tolerant struct
	// decode and never from a user-level base layer: a verifier table with an
	// unknown key must fail the load, and the profiles that judge a project's
	// gates must come from that project's file alone.
	Verifiers map[string]VerifierProfile `toml:"-"`
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
	// StreamContentIdleTimeoutSeconds bounds the gap between successive
	// CONTENT chunks on an SSE stream - a keepalive trickle does not reset
	// it. Unset (nil) resolves to DefaultStreamContentIdleTimeoutSeconds
	// (90s). This is a process-wide setting, not per-provider: mivia runs
	// one active provider configuration per process.
	StreamContentIdleTimeoutSeconds *int `toml:"stream_content_idle_timeout_seconds"`
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
	// RequestTimeoutSeconds is the per-LLM-request context deadline for root
	// AGENT turns (tools on). Unset (nil) resolves to
	// DefaultChatRequestTimeoutSeconds (900s). Plain (tools-off) chat turns
	// carry no per-request context; the stream watchdogs and the derived
	// http.Client wall bound them instead.
	RequestTimeoutSeconds *int `toml:"request_timeout_seconds"`
	// MaxUnactedContinuations bounds how many times one agent turn may be
	// continued after it announced work and then ended without calling a
	// single tool. 0 (the default) disables the mechanism. Raise it for a
	// model that narrates its plan instead of acting on it; every
	// continuation costs one extra provider call.
	MaxUnactedContinuations int `toml:"max_unacted_continuations"`
}

// TUIConfig controls terminal user interface preferences.
type TUIConfig struct {
	Theme string `toml:"theme"`
	// Mouse is the cockpit's mouse-capture default: true (default)
	// enables in-app drag-select, copy, and wheel; false hands the mouse
	// to the terminal for native selection. MIVIA_MOUSE overrides it at
	// startup; Settings → General changes it live. See
	// docs/design/cockpit-research.md rule 6.5.
	Mouse         *bool `toml:"mouse"`
	ShowReasoning *bool `toml:"show_reasoning"`
	ScrollLines   *int  `toml:"scroll_lines"`
	ScreenReader  *bool `toml:"screen_reader"`
	ReducedMotion *bool `toml:"reduced_motion"`
}

// Resolved is the fully resolved runtime config used by the CLI.
type Resolved struct {
	// RedactionPolicy is compiled during Load so an invalid pattern fails at
	// startup. Nil means the workspace configured none, which redacts nothing.
	RedactionPolicy *redact.Policy
	// MaxSteps is nil when unconfigured, so the chat default applies. A
	// configured 0 is meaningful (unlimited) and must not be confused with it.
	MaxSteps *int
	// MaxUnactedContinuations is the resolved [chat]
	// max_unacted_continuations. 0 (the default) leaves the continuation
	// mechanism off; see agent.Options.MaxUnactedContinuations.
	MaxUnactedContinuations int
	ConfigPath              string
	EnvFilePath             string
	EnvFileUsed             bool
	ProviderName            string
	Model                   string
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
	// Sync is the resolved [sync] configuration.
	Sync ResolvedSync
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
	// StreamContentIdleTimeout is the resolved [provider]
	// stream_content_idle_timeout_seconds, defaulted when unset. See
	// ProviderSection.StreamContentIdleTimeoutSeconds.
	StreamContentIdleTimeout time.Duration
	// ChatRequestTimeout is the resolved [chat] request_timeout_seconds,
	// defaulted when unset. See ChatConfig.RequestTimeoutSeconds.
	ChatRequestTimeout time.Duration
	// ProviderHTTPTimeout is the derived absolute http.Client wall for
	// provider requests: the maximum of the 15-minute floor and every
	// configured per-request budget plus the margin. See
	// resolveProviderHTTPTimeout in load.go.
	ProviderHTTPTimeout time.Duration

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
