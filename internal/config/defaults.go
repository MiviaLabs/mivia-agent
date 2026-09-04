package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// DefaultOrchestrationTimeoutSec is the finite parent-tool / batch budget used
// when default_timeout_seconds is 0 (or omitted). Long enough for multi-step
// subagent work; never unbounded so cancel/timeout always surfaces.
const DefaultOrchestrationTimeoutSec = 12 * 60 * 60 // 12 hours

// DefaultSubagentRequestTimeoutSec is the per-LLM-request context deadline for
// a subagent turn when default_request_timeout_seconds is 0 (or omitted).
// Product decision: 30 minutes. It bounds one provider request, not the whole
// task. The derived http.Client wall (resolveProviderHTTPTimeout in load.go)
// stays above this budget plus a margin, so the budget - not the wire wall -
// is what ends an overlong request.
const DefaultSubagentRequestTimeoutSec = 1800 // 30 minutes

// ResolvedSubagentRequestTimeout returns the per-LLM-request deadline for a
// subagent turn: a positive default_request_timeout_seconds is the deadline;
// anything else resolves to DefaultSubagentRequestTimeoutSec. Both the
// derived provider HTTP wall (resolveProviderHTTPTimeout in load.go) and
// internal/clichat resolve through this one helper so the two cannot drift.
func ResolvedSubagentRequestTimeout(cfg SubagentConfig) time.Duration {
	if cfg.DefaultRequestTimeoutSec > 0 {
		return SaturatingSeconds(cfg.DefaultRequestTimeoutSec)
	}
	return DefaultSubagentRequestTimeoutSec * time.Second
}

// DefaultSubagentTotalTimeoutSec is the whole-subagent wall-clock budget
// applied when default_total_timeout_seconds is 0 (or omitted). Product
// decision: 60 minutes. Unlike the per-request deadline above, this bounds
// the ENTIRE run - every request, tool call, and wait added together - so a
// provider that trickles bytes forever still ends inside a finite window.
// It is the last-resort termination guarantee; a smaller per-task timeout
// from the caller wins when it is tighter. It must stay comfortably above
// DefaultSubagentRequestTimeoutSec: the total budget is the outer context
// every per-request deadline is derived from, and a child context.WithTimeout
// can never extend past its parent's deadline - a total shorter than the
// request default would silently truncate a single legitimate call before
// it ever reached its own documented allowance.
const DefaultSubagentTotalTimeoutSec = 3600 // 60 minutes

// DefaultPromptCapTokens is a reference [chat] max_prompt_tokens value, used
// by tests as a representative operator cap. It is NOT a default and no
// longer a recommendation: one cap over a mixed catalogue holds every model
// to the smallest of them, so an unset knob - each model running to its own
// window minus its output reserve - is the normal configuration. The planner
// compacts history at 80% of whatever the budget turns out to be.
const DefaultPromptCapTokens = 200_000

// DefaultOutputReserveTokens is the completion allowance assumed when the
// operator has NOT set [chat] max_tokens.
//
// A model's max_output_tokens is a per-response CEILING, not a typical
// response size. Using it as the default reserve did two harmful things at
// once: it asked the provider for that much output on EVERY request, and -
// because the reserve is subtracted from the context window to derive the
// prompt budget - it permanently removed that much prompt capacity. On a
// 200k-window model declaring a 128k ceiling (every Claude and GLM entry in
// the shipped config) that left only 72k of usable prompt and compacted
// history at 57.6k, under a third of the window the user believed they had.
//
// The reserve itself is not optional: providers validate
// input_tokens + max_tokens <= context_window and reject the request
// outright, so the subtracted reserve and the wire max_tokens must stay in
// lockstep. Only the DEFAULT value changes here. An operator who genuinely
// wants long responses sets [chat] max_tokens explicitly, which is
// authoritative up to the model ceiling.
const DefaultOutputReserveTokens = 32_768

// MaxTimeoutSeconds is the overflow-safety ceiling for every timeout that
// EffectiveTimeoutSec returns. It is NOT a policy cap: raise-only semantics
// let a model push any effective timeout up to 10 years, far beyond any real
// task. The clamp exists so a huge model-supplied timeout_seconds (which
// parses fine and fits int64) cannot overflow time.Duration when multiplied by
// time.Second: 10 years = 3.15e17 ns << MaxInt64 (9.22e18), and even
// dispatchOrchestrationSec's +15s slack stays safe (3.15e8+15 << 9.2e9 s).
// Without it, a wrapped-negative duration is ignored by the agent loop, which
// falls back to DefaultToolTimeout (60s) - far below the operator floor.
// It is also the SaturatingSeconds ceiling (load.go), so the repo has
// exactly one overflow-safety ceiling.
const MaxTimeoutSeconds = 315_360_000 // 10 years

// defaultInlineOutputBytes is the default per-task output size threshold.
// Task results at or below this size are inlined; above it, only a ref +
// synopsis are emitted. 4096 bytes is enough for short answers to stay
// ergonomic, while longer reports (the main token cost in fan-out) go by
// reference.
const defaultInlineOutputBytes = 4096

// defaultSpawnStaggerMs is the inter-task start delay for a dispatch batch.
// 150ms is negligible against multi-second LLM calls but enough to keep N
// workers from opening their first provider connection on the same instant
// (the step-1 thundering-herd hang behind overloaded local proxies).
const defaultSpawnStaggerMs = 150

// maxSpawnStaggerMs clamps spawn_stagger_ms against misconfiguration: a typo'd
// value (seconds typed as milliseconds) must not serialize every batch. An
// explicit 0 keeps its own meaning (disabled) and is never clamped to this.
const maxSpawnStaggerMs = 1000

// MaxSchemaRetryMax is the load-time ceiling for [subagents] schema_retry_max.
// A positive configured value above it is clamped to it, so an operator typo
// (40 typed instead of 4) cannot configure a 40+-call schema-retry storm, where
// every retry is a full provider round-trip. Values <= 0 keep the existing
// "use the default 2" behavior; only 1..MaxSchemaRetryMax pass through.
const MaxSchemaRetryMax = 10

// Messaging defaults (plan 53.01). Messaging is always enabled; budgets and
// routing quotas are the only operational knobs.
const (
	defaultMessagingMaxBodyBytes         = 2048
	defaultMessagingMaxMessagesPerTask   = 32
	defaultMessagingSteerWatchdogSeconds = 300
	defaultMessagingMailboxCapacity      = 32
	defaultMessagingMaxPendingQuestions  = 1
	defaultMessagingRoutingMode          = "policy"
	defaultMessagingMaxAsksPerTask       = 4
	defaultMessagingMaxReferralDepth     = 2
	defaultMessagingMaxReferralSpawns    = 4
)

// intPtr returns a pointer to v. Local helper: the package's test files
// define their own ptr-style helpers, so a distinct name avoids colliding
// with them during test builds.
func intPtr(v int) *int { return &v }

// boolPtr returns a pointer to v, mirroring intPtr for the [memory] enabled
// knob whose nil value means "enabled" (absent key).
func boolPtr(v bool) *bool { return &v }

// DefaultMemoryConfig is the resolved default for [memory] (plan 68).
var DefaultMemoryConfig = MemoryConfig{
	StoreBackend:     "markdown",
	MaxEntryBytes:    8192,
	MaxEntries:       500,
	MaxSearchResults: 8,
}

// [memory] bounds. Below the entry floor a memory cannot hold its template;
// above the ceiling a save would dominate the store. max_search_results is
// capped so one tool call stays a small, bounded read.
const (
	MinMemoryEntryBytes    = 256
	MaxMemoryEntryBytes    = 65536
	MaxMemorySearchResults = 50
)

// DefaultMessagingConfig is the resolved default for [subagents.messaging].
var DefaultMessagingConfig = MessagingConfig{
	// Enabled left nil so IsEnabled() returns true without allocating.
	MaxBodyBytes:         defaultMessagingMaxBodyBytes,
	MaxMessagesPerTask:   defaultMessagingMaxMessagesPerTask,
	SteerWatchdogSeconds: intPtr(defaultMessagingSteerWatchdogSeconds),
	MailboxCapacity:      defaultMessagingMailboxCapacity,
	MaxPendingQuestions:  defaultMessagingMaxPendingQuestions,
	Routing: MessagingRoutingConfig{
		Mode:                    defaultMessagingRoutingMode,
		MaxAsksPerTask:          defaultMessagingMaxAsksPerTask,
		MaxReferralDepth:        defaultMessagingMaxReferralDepth,
		MaxReferralSpawnsPerRun: defaultMessagingMaxReferralSpawns,
	},
}

// Default subagent config values. All bounds default to 0 (unlimited); users
// who want caps set them in [subagents] in mivia.toml.
var DefaultSubagentConfig = SubagentConfig{
	MaxWorkers: 0,
	MaxDepth:   0,
	MaxFanout:  0,
	// 0 means "no short ceiling" at config level; runtime applies
	// DefaultOrchestrationTimeoutSec as a safety bound (see EffectiveTimeoutSec).
	DefaultTimeout:    0,
	DefaultBudget:     0,
	NestedSteps:       0,
	SystemPrompt:      "",
	MaxAuditRounds:    0, // 0 = unlimited by default
	InlineOutputBytes: defaultInlineOutputBytes,
	SchemaRetryMax:    2,
	SpawnStaggerMs:    defaultSpawnStaggerMs,
	Messaging:         DefaultMessagingConfig,
}

// DefaultWritePathBlocklist is the built-in set of workspace paths that
// workflow agent write tools refuse. It ships empty: protection is opt-in
// via [tools] write_path_blocklist, not a compiled-in default a project must
// opt out of. A project that wants .git and .mivia/mivia.toml protected
// again (recommended - see .mivia/mivia.toml.example) adds them explicitly;
// [tools] write_path_blocklist_remove still works against whatever a project
// adds, in case a future built-in entry is reintroduced.
var DefaultWritePathBlocklist = []string{}

// DefaultToolsConfig defines the built-in tool policy defaults.
var DefaultToolsConfig = ToolsConfig{
	RunTimeoutSec:     900,
	MaxReadBytes:      0,
	MaxWriteKB:        0,
	MaxOutputBytes:    0,
	MaxListDirEntries: 0,
	RedactToolArgs:    false,
	// 4 MiB is generous by design. A Tavily basic search is tens of KiB, but
	// an advanced extract of a large page returns the page content whole, and
	// the failure mode of a too-small bound is a refused request (a spent API
	// credit and no result), not a truncated one. It is also the number the
	// dispatcher's output backstop is derived from, so it is bounded rather
	// than unlimited. See MaxTavilyResponseBytes.
	MaxTavilyResponseBytes: 4 << 20,
	// 4 MiB by default. The old 1024 KiB default was too small for real pages
	// ("pages have way more than 1024 sometimes"); unlike the Tavily bound,
	// fetch_url truncates an over-bound body instead of refusing it, so an
	// operator-raised (or unlimited) value is always safe. resolveToolsConfig
	// maps an unset-or-0 knob to this default; fetch_url itself treats a 0 it
	// receives via direct construction as unlimited.
	MaxFetchKB: 4096,
	// 0 (uncapped) by default - the agent loop's own result cap
	// (max_tool_result_bytes) is the operator-configurable ceiling.
	// OOM guard for uncapped volume tools; not a context-cost cap.
	MemoryBackstopMB: 256,
	// 64 KiB: enough for dozens of matches with short context windows while
	// keeping inspect_repository's result a small, always-bounded JSON
	// envelope. See MinInspectRepositoryBytes / MaxInspectRepositoryBytesLimit.
	MaxInspectRepositoryBytes: 64 << 10,
}

// Bounds for [tools] max_inspect_repository_bytes. Below the floor the fixed
// JSON envelope (provenance + one result) cannot fit; above the ceiling the
// tool stops being a small, bounded read.
const (
	MinInspectRepositoryBytes      = 4 << 10
	MaxInspectRepositoryBytesLimit = 256 << 10
)

// DefaultMemoryBackstopMB is the shipped OOM guard when memory_backstop_mb is
// unset or non-positive (cannot be accidentally disabled via 0).
const DefaultMemoryBackstopMB = 256

// MemoryBackstopBytes converts a memory backstop in megabytes to bytes.
func MemoryBackstopBytes(memoryBackstopMB int) int {
	return memoryBackstopMB << 20
}

// UsefulToolResultRequestBytes is a practical upper bound for a single
// provider-request tool-result carry size. A nonzero max_tool_result_bytes
// above this is accepted but warned (never clamped).
const UsefulToolResultRequestBytes = 4 << 20

// Tavily response bound limits. Below the floor every legitimate response
// fails; above the ceiling, budget + input allowance + framing slack risks
// overflowing the dispatcher's ceiling derivation, which would silently drop
// the backstop to its floor while the wire read stayed effectively infinite.
const (
	MinTavilyResponseBytes = 1024
	MaxTavilyResponseLimit = 64 << 20
)

// Aggregate per-batch tool-result budget knob values ([tools]
// batch_result_budget_bytes). The agent loop owns the enforcement; these are
// the operator-facing constants its config surface is validated against.
const (
	// BatchResultBudgetOff disables the mechanism (the default).
	BatchResultBudgetOff = 0
	// BatchResultBudgetDerived derives the budget from the model's prompt
	// budget instead of naming a number.
	BatchResultBudgetDerived = -1
	// MinBatchResultBudgetBytes is the smallest literal budget that can hold:
	// it matches the loop's degrade floor, below which the first oversized
	// result overshoots by construction.
	MinBatchResultBudgetBytes = 16 << 10
)

// EffectiveTimeoutSec returns a positive timeout in seconds for subagent /
// orchestration work. configured is DefaultTimeout or a batch/task override;
// when configured is <= 0, DefaultOrchestrationTimeoutSec is used as the
// floor so work cannot hang forever. The function is raise-only: overrides
// can push the effective timeout up, never below the configured floor. A
// smaller positive override does not shrink the budget; when several are
// supplied, the largest override bounds the enclosing operation.
//
// EffectiveTimeoutSec is the right helper for fallback budgets (recovery,
// unset overrides, per-task floors relative to a batch). For explicit
// caller-requested timeouts that should be honored as the actual budget —
// not floored to the 12h default — use RequestedTimeoutSec instead.
//
// The result is clamped to MaxTimeoutSeconds. This is an overflow-safety
// clamp, not a policy cap: raise-only semantics still hold for every value
// below 10 years, and every downstream time.Duration(n)*time.Second stays
// positive (see MaxTimeoutSeconds).
func EffectiveTimeoutSec(configured int, overrides ...int) int {
	base := configured
	if base <= 0 {
		base = DefaultOrchestrationTimeoutSec
	}
	for _, o := range overrides {
		if o > base {
			base = o
		}
	}
	if base > MaxTimeoutSeconds {
		return MaxTimeoutSeconds
	}
	return base
}

// RequestedTimeoutSec returns the timeout budget when the caller provides an
// explicit timeout_seconds value. Unlike EffectiveTimeoutSec's raise-only floor,
// an explicit positive value IS the budget — it is not floored to the
// configured default or DefaultOrchestrationTimeoutSec. This lets the root
// orchestrator bound a dispatch_tasks batch or delegate call to a shorter
// window than the global default, which is the intended semantics of the
// timeout_seconds parameter on orchestration tools.
//
// When explicit is 0 or negative ("use the default"), the configured default
// applies via EffectiveTimeoutSec, preserving backward compatibility.
// taskOverrides may still raise the budget above the explicit value: a task
// may legitimately need more than the batch budget, and the whole-call budget
// must accommodate the longest task. The result is clamped to MaxTimeoutSeconds.
func RequestedTimeoutSec(configured int, explicit int, taskOverrides ...int) int {
	base := explicit
	if base <= 0 {
		base = EffectiveTimeoutSec(configured)
	}
	for _, o := range taskOverrides {
		if o > base {
			base = o
		}
	}
	if base > MaxTimeoutSeconds {
		return MaxTimeoutSeconds
	}
	return base
}

// Built-in provider defaults.
const (
	DefaultProvider  = "openrouter"
	DeepSeekProModel = "deepseek-v4-pro"
)

// defaultStorePath returns the default SQLite database path for
// the orchestration ledger on the current platform.
// Uses the current working directory as a workspace identifier so each
// project gets its own database file automatically.
func defaultStorePath() string {
	cwd, err := os.Getwd()
	if err == nil && cwd != "" {
		return DefaultStorePathForWorkspace(cwd)
	}
	return DefaultStorePathForWorkspace("")
}

// IsDefaultOrchestrationStorePath reports whether path is the config-layer
// default orchestration-ledger location (see defaultStorePath): the one tier
// whose directory no operator manages, so opens may harden it 0600/0700. An
// operator-configured store_path compares false and keeps its modes.
func IsDefaultOrchestrationStorePath(path string) bool {
	return path == defaultStorePath()
}

// DefaultStorePathForWorkspace returns the default SQLite path for root.
func DefaultStorePathForWorkspace(root string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	if root != "" {
		safe := sanitizePath(root)
		return filepath.Join(dir, "mivia", "workspaces", safe, "orchestration.db")
	}
	return filepath.Join(dir, "mivia", "orchestration.db")
}

// sanitizePath converts a path into a safe filesystem directory name.
func sanitizePath(path string) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("ws-%x", h[:8])
}

// TempStorePath returns the OS-temp-dir path for an ad-hoc (no project
// config found) store named name, hash-keyed by root via the existing
// sanitizePath helper (see DefaultStorePathForWorkspace) so distinct
// ad-hoc roots never collide. Rooted at os.TempDir(), not
// os.UserCacheDir() like DefaultStorePathForWorkspace: an ad-hoc run
// names no real project to key a durable, indefinitely-retained cache
// entry against, so normal OS temp cleanup can reclaim it instead of
// silently accumulating forever under the user's real home/cache.
func TempStorePath(root, name string) string {
	return filepath.Join(os.TempDir(), workspace.Namespace, "adhoc", sanitizePath(root), name+".db")
}
