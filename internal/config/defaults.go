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
const DefaultOrchestrationTimeoutSec = 7200 // 2 hours

// Default subagent config values.
var DefaultSubagentConfig = SubagentConfig{
	MaxWorkers: 4,
	MaxDepth:   3,
	MaxFanout:  16,
	// 0 means "no short ceiling" at config level; runtime applies
	// DefaultOrchestrationTimeoutSec as a safety bound (see EffectiveTimeoutSec).
	DefaultTimeout: 0,
	DefaultBudget:  0,
	PartialResults: false,
	NestedSteps:    100,
	SystemPrompt:   "",
	MaxAuditRounds: 5, // default cap for ADLC Step 5 audit loop
}

// EffectiveTimeoutSec returns a positive timeout in seconds for subagent /
// orchestration work. configured is DefaultTimeout or a batch/task override;
// when both configured and override are <= 0, DefaultOrchestrationTimeoutSec
// is used so work cannot hang forever. The larger of configured and override
// wins when either is positive (callers that need a single value pass one).
func EffectiveTimeoutSec(configured int, overrides ...int) int {
	max := configured
	for _, o := range overrides {
		if o > max {
			max = o
		}
	}
	if max <= 0 {
		return DefaultOrchestrationTimeoutSec
	}
	return max
}

// Built-in provider defaults.
const (
	DefaultProvider = "deepseek"

	DeepSeekName         = "deepseek"
	DeepSeekDefaultModel = "deepseek-v4-flash"
	DeepSeekProModel     = "deepseek-v4-pro"
	DeepSeekDefaultURL   = "https://api.deepseek.com/v1"
	DeepSeekAPIKeyEnv    = "DEEPSEEK_API_KEY"

	OpenRouterName         = "openrouter"
	OpenRouterDefaultModel = "openai/gpt-4o-mini"
	OpenRouterDefaultURL   = "https://openrouter.ai/api/v1"
	OpenRouterAPIKeyEnv    = "OPENROUTER_API_KEY"
)

// KnownProviders lists supported provider names.
var KnownProviders = []string{DeepSeekName, OpenRouterName}

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
