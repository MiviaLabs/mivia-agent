package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

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
	// nil (absent) resolves to the derived budget; explicit 0 disables it;
	// any negative derives it from the model's prompt budget (a quarter of
	// it, floor 256 KiB; inert when there is no prompt budget). A positive
	// value is the literal byte budget and must be at least 16 KiB - below
	// that every batch would degrade to references.
	//
	// Over-budget results are degraded to content references, never failed:
	// the model already paid for the calls and their side effects already
	// happened.
	BatchResultBudgetBytes *int `toml:"batch_result_budget_bytes"`
	// RefOnlyTools is an opt-in list of tool names whose results are always
	// spooled and replaced by a ref-only notice, never inlined whole; empty
	// = off.
	RefOnlyTools []string `toml:"ref_only_tools"`
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
	// WritePathBlocklist adds workspace-relative paths or directories whose
	// write tools refuse to change. It extends the built-in defaults
	// (DefaultWritePathBlocklist: .git, .mivia/mivia.toml); it cannot remove
	// them. Entries use forward slashes and are normalized (trimmed, cleaned)
	// at load; an entry that is empty, ".", or absolute is a load error.
	// The blocklist applies to workflow agent steps, whose write tools
	// (write_file, search_replace, multi_edit, delete_file) refuse listed
	// paths. The interactive session registry does not enforce it.
	WritePathBlocklist []string `toml:"write_path_blocklist,omitempty"`
	// MaxInspectRepositoryBytes caps the inspect_repository result envelope
	// (bytes). Unlike MaxReadBytes/MaxOutputBytes there is no uncapped state:
	// the tool's output must always be valid, bounded JSON. 0 or negative
	// resolves to the built-in 64 KiB default. Values outside
	// [MinInspectRepositoryBytes, MaxInspectRepositoryBytes] are rejected at load.
	MaxInspectRepositoryBytes int `toml:"max_inspect_repository_bytes"`
	// DiagnosticsCommand is the DEPRECATED alias for the reserved "default"
	// entry of DiagnosticsCommands. It is the argv of the project diagnostics
	// command the get_diagnostics tool runs. resolveToolsConfig folds a set
	// alias into DiagnosticsCommands["default"] and then clears it, so
	// Validate (and every consumer) sees exactly one surface; setting BOTH
	// the alias and the map is ambiguous and rejected at load. Empty (unset
	// or []) means the tool is not registered. When set, argv[0] must be a
	// bare program name on the resolved run allowlist (run_allowlist_only
	// when set, else run_allowlist); validation rejects anything else at
	// load, mirroring the run_command gate, so a diagnostics tool that could
	// never register cannot load clean.
	DiagnosticsCommand []string `toml:"diagnostics_command"`
	// DiagnosticsCommands maps a command name to the argv of a project
	// diagnostics command the get_diagnostics tool runs. Empty or unset means
	// the tool is not registered; "default" is the reserved name for the
	// deprecated diagnostics_command alias (declaring it explicitly is
	// allowed). Every entry is validated at load like the run_command gate:
	// non-empty argv, argv[0] a bare program name on the EFFECTIVE run
	// allowlist (run_allowlist_only when set, else run_allowlist, minus
	// run_blocklist - the tools layer subtracts the blocklist in
	// configuredRunAllowlist, so a command that could never register is a
	// load error). Command names must be non-empty, non-whitespace, and
	// unique after case folding.
	DiagnosticsCommands map[string][]string `toml:"diagnostics_commands"`
}

// validateTools runs every [tools] validation. It is the single entry point
// called from Resolved.Validate so the validation surface stays in one file.
func validateTools(tc ToolsConfig) error {
	if err := validateToolResultBudgets(tc); err != nil {
		return err
	}
	if err := validateDiagnosticsCommands(tc); err != nil {
		return err
	}
	return validateWritePathBlocklist(tc)
}

// validateDiagnosticsCommands validates the whole [tools] diagnostics_commands
// map (the ONE surface; the deprecated diagnostics_command alias has already
// been folded into DiagnosticsCommands["default"] and cleared by
// resolveToolsConfig when it was set alone). It enforces, at load, the same
// gate the run_command tool applies at runtime (internal/tools/run.go
// resolveAllowedCommand), so a diagnostics command whose get_diagnostics tool
// could never register is a load error instead of a silently absent tool. The
// membership logic is mirrored locally - the config package must not import
// internal/tools.
//
// STE: the deprecated alias and the map are the same surface; setting both is
// ambiguous and must be a LOAD ERROR, not a silent precedence choice.
// resolveToolsConfig cannot return an error, so it leaves the both-set case
// intact for this check.
//
// STE: map keys are command names the model selects by. Empty, whitespace-only,
// and case-folded duplicate keys cannot be selected unambiguously and are load
// errors. (TOML keys are case-sensitive, so "Lint" and "lint" both parse; the
// case-folded collision is this layer's rejection, not the parser's.)
//
// STE: the effective allowlist is run_allowlist_only when set, else
// run_allowlist, MINUS run_blocklist (case-folded subtraction, mirroring the
// tools layer's disabledToolNames in configuredRunAllowlist). resolveToolsConfig
// has already cleared RunAllowlist when RunAllowlistOnly is set (B7, before
// Validate runs), so that selection is exactly what the tools layer resolves,
// and a command allowlisted only in the non-authoritative run_allowlist - or
// blocklisted - is refused.
//
// STE: an unset or empty map is a no-op (the get_diagnostics tool is simply not
// registered), keeping every pre-existing config loading unchanged - backward
// compatibility is a hard contract.
func validateDiagnosticsCommands(tc ToolsConfig) error {
	if len(tc.DiagnosticsCommand) > 0 && len(tc.DiagnosticsCommands) > 0 {
		return fmt.Errorf("[tools] diagnostics_command and diagnostics_commands are both set; diagnostics_commands is the one surface - remove the deprecated diagnostics_command alias")
	}
	effective := effectiveDiagnosticsAllowlist(tc)
	// Keys first, so a config with an invalid name fails on the name before
	// any per-entry argv check obscures it.
	seen := make(map[string]string, len(tc.DiagnosticsCommands))
	for name := range tc.DiagnosticsCommands {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("[tools] diagnostics_commands command name %q is empty or whitespace-only; names must be non-empty", name)
		}
		folded := strings.ToLower(name)
		if prev, dup := seen[folded]; dup {
			return fmt.Errorf("[tools] diagnostics_commands command names %q and %q collide after case folding; names must be unique ignoring case", prev, name)
		}
		seen[folded] = name
	}
	for name, argv := range tc.DiagnosticsCommands {
		if err := validateDiagnosticsCommandEntry(name, argv, effective); err != nil {
			return err
		}
	}
	return nil
}

// validateDiagnosticsCommandEntry validates ONE diagnostics_commands entry like
// the run_command gate: non-empty argv, a bare argv[0] (no path separator), and
// argv[0] on the effective run allowlist.
//
// STE: a path-shaped argv[0] ("C:\\Tools\\diag.exe", "/bin/sh") is never a
// bare name on the allowlist; the run_command gate refuses it, so the config
// layer must refuse the same value at load.
func validateDiagnosticsCommandEntry(name string, argv []string, effectiveAllowlist []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("[tools] diagnostics_commands[%q] argv must be non-empty, got %v", name, argv)
	}
	bin := argv[0]
	if bin == "" {
		return fmt.Errorf("[tools] diagnostics_commands[%q] argv[0] must be non-empty, got %v", name, argv)
	}
	if strings.Contains(bin, string(os.PathSeparator)) || strings.Contains(bin, "/") || strings.Contains(bin, "\\") {
		return fmt.Errorf("[tools] diagnostics_commands[%q] argv[0] must be a bare name on the run allowlist, not a path: %q", name, bin)
	}
	if !allowlisted(bin, effectiveAllowlist) {
		return fmt.Errorf("[tools] diagnostics_commands[%q] argv[0] %q is not on the effective run allowlist (allowed: %s)", name, bin, strings.Join(effectiveAllowlist, ", "))
	}
	return nil
}

// effectiveDiagnosticsAllowlist mirrors internal/tools/default_registry.go
// configuredRunAllowlist: run_allowlist_only when set (resolveToolsConfig's B7
// rule has already cleared RunAllowlist), else run_allowlist, MINUS
// run_blocklist. The blocklist subtraction case-folds both sides exactly like
// the tools layer's disabledToolNames, so "SH" blocks "sh" the same way the
// run_command registry would refuse it.
func effectiveDiagnosticsAllowlist(tc ToolsConfig) []string {
	allowlist := tc.RunAllowlistOnly
	if len(allowlist) == 0 {
		allowlist = tc.RunAllowlist
	}
	if len(tc.RunBlocklist) == 0 {
		return allowlist
	}
	blocked := make(map[string]bool, len(tc.RunBlocklist))
	for _, name := range tc.RunBlocklist {
		blocked[strings.ToLower(name)] = true
	}
	filtered := make([]string, 0, len(allowlist))
	for _, entry := range allowlist {
		if blocked[strings.ToLower(entry)] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// allowlisted mirrors internal/tools/run.go allowed(): case-folded comparison
// of the program name (and its base name, for exactness) against each
// allowlist entry, which the tools layer stores pre-lowercased.
func allowlisted(bin string, allowlist []string) bool {
	base := filepath.Base(bin)
	binLower := strings.ToLower(bin)
	baseLower := strings.ToLower(base)
	for _, entry := range allowlist {
		e := strings.ToLower(entry)
		if e == binLower || e == baseLower {
			return true
		}
	}
	return false
}

// validateWritePathBlocklist rejects entries that cannot protect anything:
// entries that clean to "." (empty, whitespace-only, "x/..") or absolute
// paths are silent no-ops in isWriteDeniedPath, so a misconfigured blocklist
// fails closed at load instead of failing open at write time.
func validateWritePathBlocklist(tc ToolsConfig) error {
	for _, entry := range tc.WritePathBlocklist {
		trimmed := strings.TrimSpace(entry)
		cleaned := filepath.Clean(trimmed)
		if cleaned == "." {
			return fmt.Errorf("[tools] write_path_blocklist entry %q is empty or resolves to the workspace root; use a relative path", entry)
		}
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("[tools] write_path_blocklist entry %q escapes the workspace; use a relative path inside the workspace", entry)
		}
		if filepath.IsAbs(cleaned) {
			return fmt.Errorf("[tools] write_path_blocklist entry %q is absolute; use a workspace-relative path", entry)
		}
		// A backslash separator is a single filename character on non-Windows
		// hosts, so an entry written with Windows separators can never match
		// a real workspace-relative path there. Reject it instead of letting
		// the protection silently not exist.
		if runtime.GOOS != "windows" && strings.Contains(trimmed, "\\") {
			return fmt.Errorf("[tools] write_path_blocklist entry %q uses a backslash separator; use forward slashes", entry)
		}
	}
	return nil
}

// Validation for the two [tools] knobs that bound tool-result bytes: the
// per-call ceiling and the aggregate per-batch budget. Both reject values that
// would leave the model with results too small to use, rather than accepting a
// number and quietly producing stubs.

func resolveToolsConfig(tc ToolsConfig) ToolsConfig {
	tc = resolveToolsDefaults(tc)
	// No defaulting: 0 means uncapped. Negative is normalized to 0 so every
	// consumer can treat <=0 uniformly as "no cap".
	if tc.MaxToolResultBytes < 0 {
		tc.MaxToolResultBytes = 0
	}
	// Absent resolves to derived (-1) so an operator who configures nothing
	// gets the derived batch budget; explicit 0 is off; negative is derived;
	// positive is literal.
	if tc.BatchResultBudgetBytes == nil || *tc.BatchResultBudgetBytes < 0 {
		derived := BatchResultBudgetDerived
		tc.BatchResultBudgetBytes = &derived
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
	tc = resolveDiagnosticsAlias(tc)
	// Normalize write_path_blocklist entries so the write tools compare exact
	// workspace-relative paths: trim whitespace, collapse separators, use
	// forward slashes. Defaults are NOT injected here; the workflow registry
	// composes DefaultWritePathBlocklist + additions at build time.
	if len(tc.WritePathBlocklist) > 0 {
		norm := make([]string, 0, len(tc.WritePathBlocklist))
		for _, entry := range tc.WritePathBlocklist {
			norm = append(norm, filepath.ToSlash(filepath.Clean(strings.TrimSpace(entry))))
		}
		tc.WritePathBlocklist = slices.Clone(norm)
	}
	tc = normalizeRefOnlyTools(tc)
	return tc
}

// resolveToolsDefaults fills the byte and count knobs whose shared rule is
// "unset or non-positive resolves to the built-in default". Each knob's
// comment explains why its particular state cannot pass through: Tavily
// responses must declare a budget, fetch_url's unlimited state is
// indistinguishable from an unset 0 at the TOML boundary, and
// inspect_repository always needs valid bounded JSON.
func resolveToolsDefaults(tc ToolsConfig) ToolsConfig {
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
	return tc
}

// normalizeRefOnlyTools normalizes ref_only_tools: trim whitespace, drop
// empty strings, dedupe preserving order. Entries are matched exactly
// (case-sensitive) by the consumer, so dedupe is exact-string too.
func normalizeRefOnlyTools(tc ToolsConfig) ToolsConfig {
	if len(tc.RefOnlyTools) > 0 {
		seen := make(map[string]struct{}, len(tc.RefOnlyTools))
		norm := make([]string, 0, len(tc.RefOnlyTools))
		for _, name := range tc.RefOnlyTools {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" {
				continue
			}
			if _, dup := seen[trimmed]; dup {
				continue
			}
			seen[trimmed] = struct{}{}
			norm = append(norm, trimmed)
		}
		tc.RefOnlyTools = norm
	}
	return tc
}

// resolveDiagnosticsAlias folds the deprecated DiagnosticsCommand alias into
// the reserved "default" entry of DiagnosticsCommands and clears the alias, so
// Validate (and every consumer) sees exactly one surface - the same shape as
// the B7 clears above. Both set is ambiguous and must be a load error, but
// resolveToolsConfig cannot return one, so the both-set case is left intact
// for validateDiagnosticsCommands to reject.
func resolveDiagnosticsAlias(tc ToolsConfig) ToolsConfig {
	if len(tc.DiagnosticsCommand) > 0 && len(tc.DiagnosticsCommands) == 0 {
		if tc.DiagnosticsCommands == nil {
			tc.DiagnosticsCommands = make(map[string][]string, 1)
		}
		tc.DiagnosticsCommands["default"] = tc.DiagnosticsCommand
		tc.DiagnosticsCommand = nil
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
	// Reject it rather than ship a bound that only pretends to hold. A nil
	// field (absent key) is not a budget and skips the check - resolveToolsConfig
	// has already resolved it to derived before Validate runs.
	if v := tc.BatchResultBudgetBytes; v != nil && *v > 0 && *v < MinBatchResultBudgetBytes {
		return fmt.Errorf("[tools] batch_result_budget_bytes must be 0 (off), %d (derive from the prompt budget), or >= %d, got %d",
			BatchResultBudgetDerived, MinBatchResultBudgetBytes, *v)
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
