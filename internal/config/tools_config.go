package config

import "fmt"

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
	// BatchResultBudgetBytes bounds what ONE tool batch may add to history,
	// across all of its parallel calls together. max_tool_result_bytes bounds
	// each call in isolation and cannot see the others, so N calls that are
	// each honestly under it still blow the context when they land in the same
	// step; this is the only bound that sees the batch as a whole.
	//
	// 0 (the default) disables it. -1 derives it from the model's prompt
	// budget (a quarter of it, floor 256 KiB; inert when there is no prompt
	// budget). A positive value is the literal byte budget and must be at
	// least 16 KiB - below that every batch would degrade to references.
	//
	// Over-budget results are degraded to content references, never failed:
	// the model already paid for the calls and their side effects already
	// happened.
	BatchResultBudgetBytes int `toml:"batch_result_budget_bytes"`
	// MaxTavilyResponseBytes bounds the bytes read from a Tavily API response
	// body - the `search` tool's Tavily path and `extract`. It is NOT a
	// truncation cap: the tools never cut content. The bound exists so their
	// maximum output is a finite, declarable number, which the dispatcher's
	// output backstop is derived from; a response over the bound is refused
	// with an explicit error naming this key. 0 or negative resolves to the
	// built-in default (never "unlimited" - an unlimited response could not be
	// declared, and undeclared output is what the backstop destroys). Values
	// outside [1024, 64 MiB] are rejected at load.
	MaxTavilyResponseBytes int `toml:"max_tavily_response_bytes"`
	// MaxFetchKB bounds the body read by fetch_url (KiB). Default 4096 (4 MiB).
	// 0 means unlimited. Unlimited is safe for fetch_url because it truncates
	// an over-bound body instead of refusing it - unlike the Tavily bound, an
	// unbounded read still yields a bounded, usable result, so nothing the
	// dispatcher derives from a declared budget depends on this number.
	MaxFetchKB int `toml:"max_fetch_kb"`
	// MemoryBackstopMB is the OOM guard (MiB) for tools that may load whole
	// files into memory when volume caps are uncapped (read/edit/list budgets).
	// Shipped default 256. This is NOT a context-cost cap. 0 or negative
	// resolves to the default so the guard cannot be accidentally disabled.
	MemoryBackstopMB int `toml:"memory_backstop_mb"`
	// RedactToolArgs hides argv from operator-visible output.
	RedactToolArgs bool `toml:"redact_tool_args"`
	// SecretPathPatterns replaces the hard-coded secret path blocklist.
	SecretPathPatterns []string `toml:"secret_path_patterns,omitempty"`
	// SecretPathExceptions adds exceptions to the secret path blocklist.
	SecretPathExceptions []string `toml:"secret_path_exceptions,omitempty"`
	// Core is the always-advertised tool tier (plan tools/05). nil (the key
	// omitted) keeps every authorized tool core, which is byte-identical to the
	// behavior before deferred loading existed. When set, tools outside it are
	// deferred: their schemas are withheld until the model loads them with
	// load_tools. Naming a tool here never grants authority - the list is
	// intersected with the agent's effective tool set.
	Core *[]string `toml:"core,omitempty"`
	// SearchIgnorePatterns adds directory/file names to skip during grep/glob walks.
	// Extends the built-in defaults (.git, node_modules, vendor). Does not replace them.
	SearchIgnorePatterns []string `toml:"search_ignore_patterns,omitempty"`
	// MaxInspectRepositoryBytes caps the inspect_repository result envelope
	// (bytes). Unlike MaxReadBytes/MaxOutputBytes there is no uncapped state:
	// the tool's output must always be valid, bounded JSON. 0 or negative
	// resolves to the built-in 64 KiB default. Values outside
	// [MinInspectRepositoryBytes, MaxInspectRepositoryBytes] are rejected at load.
	MaxInspectRepositoryBytes int `toml:"max_inspect_repository_bytes"`
}

// Validation for the two [tools] knobs that bound tool-result bytes: the
// per-call ceiling and the aggregate per-batch budget. Both reject values that
// would leave the model with results too small to use, rather than accepting a
// number and quietly producing stubs.

func resolveToolsConfig(tc ToolsConfig) ToolsConfig {
	def := DefaultToolsConfig
	if tc.RunTimeoutSec <= 0 {
		tc.RunTimeoutSec = def.RunTimeoutSec
	}
	if tc.MaxReadBytes <= 0 {
		tc.MaxReadBytes = def.MaxReadBytes
	}
	if tc.MaxWriteKB <= 0 {
		tc.MaxWriteKB = def.MaxWriteKB
	}
	if tc.MaxOutputBytes <= 0 {
		tc.MaxOutputBytes = def.MaxOutputBytes
	}
	if tc.MaxListDirEntries <= 0 {
		tc.MaxListDirEntries = def.MaxListDirEntries
	}
	// No defaulting: 0 means uncapped. Negative is normalized to 0 so every
	// consumer can treat <=0 uniformly as "no cap".
	if tc.MaxToolResultBytes < 0 {
		tc.MaxToolResultBytes = 0
	}
	// No defaulting either: 0 is off, -1 is derived, positive is literal. Any
	// other negative is normalized to the derived sentinel so consumers can
	// treat "< 0" uniformly - Validate rejects the values that are typos
	// rather than intent.
	if tc.BatchResultBudgetBytes < 0 {
		tc.BatchResultBudgetBytes = BatchResultBudgetDerived
	}
	// Unlike MaxToolResultBytes there is no "uncapped" state: the tools that
	// read Tavily responses declare this number as their result budget, and an
	// undeclared budget is exactly what the dispatcher's backstop destroys.
	if tc.MaxTavilyResponseBytes <= 0 {
		tc.MaxTavilyResponseBytes = def.MaxTavilyResponseBytes
	}
	// Unlike the Tavily bound there IS a valid unlimited state for fetch_url:
	// it truncates an over-bound body instead of refusing it, so an unbounded
	// read still yields a bounded, usable result. But Go cannot tell an unset
	// knob from an explicit 0 (both decode to the zero value), so a <= 0 here
	// resolves to the built-in default - exactly like MaxTavilyResponseBytes.
	// fetch_url itself preserves a 0 it receives via direct construction as
	// unlimited (see internal/tools/fetch_url.go).
	if tc.MaxFetchKB <= 0 {
		tc.MaxFetchKB = def.MaxFetchKB
	}
	// Like MaxTavilyResponseBytes: no valid "unlimited" state, so <=0 (unset or
	// a typo'd negative) resolves to the built-in default rather than passing
	// through.
	if tc.MaxInspectRepositoryBytes <= 0 {
		tc.MaxInspectRepositoryBytes = def.MaxInspectRepositoryBytes
	}
	// OOM guard: 0 / negative must not disable the backstop.
	if tc.MemoryBackstopMB <= 0 {
		tc.MemoryBackstopMB = DefaultMemoryBackstopMB
	}
	// B7: RunAllowlist + RunAllowlistOnly are mutually exclusive - prefer RunAllowlistOnly
	if len(tc.RunAllowlist) > 0 && len(tc.RunAllowlistOnly) > 0 {
		tc.RunAllowlist = nil
	}
	// B7: EnvAllowlist + EnvAllowlistOnly are mutually exclusive - prefer EnvAllowlistOnly
	if len(tc.EnvAllowlist) > 0 && len(tc.EnvAllowlistOnly) > 0 {
		tc.EnvAllowlist = nil
	}
	return tc
}

func validateToolResultBudgets(tc ToolsConfig) error {
	// A positive cap below 1024 bytes starves every tool envelope (error
	// strings, JSON framing) and yields useless truncated stubs; reject it
	// rather than let the loop silently destroy every result.
	if tc.MaxToolResultBytes > 0 && tc.MaxToolResultBytes < 1024 {
		return fmt.Errorf("[tools] max_tool_result_bytes must be 0 (uncapped) or >= 1024, got %d",
			tc.MaxToolResultBytes)
	}
	// A batch budget under the degrade floor cannot be honoured: the first
	// result that does not fit is re-cut to the floor anyway, so every batch
	// would overshoot and every result after it would be a bare reference.
	// Reject it rather than ship a bound that only pretends to hold.
	if v := tc.BatchResultBudgetBytes; v > 0 && v < MinBatchResultBudgetBytes {
		return fmt.Errorf("[tools] batch_result_budget_bytes must be 0 (off), %d (derive from the prompt budget), or >= %d, got %d",
			BatchResultBudgetDerived, MinBatchResultBudgetBytes, v)
	}
	// inspect_repository's envelope must always be valid, bounded JSON: too
	// small and the framing (provenance + one result) cannot fit; too large
	// and it stops being a bounded read. resolveToolsConfig already replaced
	// an unset-or-negative value with the built-in default before this runs,
	// so a value seen here out of range is an explicit operator setting.
	if v := tc.MaxInspectRepositoryBytes; v < MinInspectRepositoryBytes || v > MaxInspectRepositoryBytesLimit {
		return fmt.Errorf("[tools] max_inspect_repository_bytes must be between %d and %d, got %d",
			MinInspectRepositoryBytes, MaxInspectRepositoryBytesLimit, v)
	}
	return nil
}
