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

// defaultInlineOutputBytes is the default per-task output size threshold.
// Task results at or below this size are inlined; above it, only a ref +
// synopsis are emitted. 4096 bytes is enough for short answers to stay
// ergonomic, while longer reports (the main token cost in fan-out) go by
// reference.
const defaultInlineOutputBytes = 4096

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
}

// Tavily response bound limits. Below the floor every legitimate response
// fails; above the ceiling, budget + input allowance + framing slack risks
// overflowing the dispatcher's ceiling derivation, which would silently drop
// the backstop to its floor while the wire read stayed effectively infinite.
const (
	MinTavilyResponseBytes = 1024
	MaxTavilyResponseLimit = 64 << 20
)

// EffectiveTimeoutSec returns a positive timeout in seconds for subagent /
// orchestration work. configured is DefaultTimeout or a batch/task override;
// when both configured and override are <= 0, DefaultOrchestrationTimeoutSec
// is used so work cannot hang forever. An explicit positive override wins over
// the configured default; when several are supplied, the largest override
// bounds the enclosing operation.
func EffectiveTimeoutSec(configured int, overrides ...int) int {
	max := 0
	for _, o := range overrides {
		if o > max {
			max = o
		}
	}
	if max <= 0 {
		max = configured
	}
	if max <= 0 {
		return DefaultOrchestrationTimeoutSec
	}
	return max
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
