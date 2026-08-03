package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// DefaultOrchestrationTimeoutSec is the finite parent-tool / batch budget used
// when default_timeout_seconds is 0 (or omitted). Long enough for multi-step
// subagent work; never unbounded so cancel/timeout always surfaces.
const DefaultOrchestrationTimeoutSec = 12 * 60 * 60 // 12 hours

// MaxTimeoutSeconds is the overflow-safety ceiling for every timeout that
// EffectiveTimeoutSec returns. It is NOT a policy cap: raise-only semantics
// let a model push any effective timeout up to 10 years, far beyond any real
// task. The clamp exists so a huge model-supplied timeout_seconds (which
// parses fine and fits int64) cannot overflow time.Duration when multiplied by
// time.Second: 10 years = 3.15e17 ns << MaxInt64 (9.22e18), and even
// dispatchOrchestrationSec's +15s slack stays safe (3.15e8+15 << 9.2e9 s).
// Without it, a wrapped-negative duration is ignored by the agent loop, which
// falls back to DefaultToolTimeout (60s) - far below the operator floor.
const MaxTimeoutSeconds = 315_360_000 // 10 years

// defaultInlineOutputBytes is the default per-task output size threshold.
// Task results at or below this size are inlined; above it, only a ref +
// synopsis are emitted. 4096 bytes is enough for short answers to stay
// ergonomic, while longer reports (the main token cost in fan-out) go by
// reference.
const defaultInlineOutputBytes = 4096

// Messaging defaults (plan 53.01). Messaging is always enabled; budgets and
// routing quotas are the only operational knobs.
const (
	defaultMessagingMaxBodyBytes        = 2048
	defaultMessagingMaxMessagesPerTask  = 32
	defaultMessagingMailboxCapacity     = 32
	defaultMessagingMaxPendingQuestions = 1
	defaultMessagingRoutingMode         = "policy"
	defaultMessagingMaxAsksPerTask      = 4
	defaultMessagingMaxReferralDepth    = 2
	defaultMessagingMaxReferralSpawns   = 4
)

// DefaultMessagingConfig is the resolved default for [subagents.messaging].
var DefaultMessagingConfig = MessagingConfig{
	// Enabled left nil so IsEnabled() returns true without allocating.
	MaxBodyBytes:        defaultMessagingMaxBodyBytes,
	MaxMessagesPerTask:  defaultMessagingMaxMessagesPerTask,
	MailboxCapacity:     defaultMessagingMailboxCapacity,
	MaxPendingQuestions: defaultMessagingMaxPendingQuestions,
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
	Messaging:         DefaultMessagingConfig,
}

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
}

// DefaultMemoryBackstopMB is the shipped OOM guard when memory_backstop_mb is
// unset or non-positive (cannot be accidentally disabled via 0).
const DefaultMemoryBackstopMB = 256

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

// Built-in provider defaults.
const (
	DefaultProvider  = "deepseek"
	DeepSeekProModel = "deepseek-v4-pro"
)

// defaultStorePath returns the default SQLite database path for
// the orchestration ledger on the current platform.
// Uses the current working directory as a workspace identifier so each
// project gets its own database file automatically.
func defaultStorePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	// Check if we can determine a workspace ID from CWD
	cwd, err := os.Getwd()
	if err == nil && cwd != "" {
		safe := sanitizePath(cwd)
		return filepath.Join(dir, "mivia", "workspaces", safe, "orchestration.db")
	}
	return filepath.Join(dir, "mivia", "orchestration.db")
}

// sanitizePath converts a path into a safe filesystem directory name.
func sanitizePath(path string) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("ws-%x", h[:8])
}
