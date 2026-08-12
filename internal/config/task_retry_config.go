package config

// TaskRetryConfig is the [subagents.retry] surface for whole-task retry.
// Fields mirror coordinator.RetryPolicy; internal/config cannot import
// internal/coordinator (coordinator already imports config), so the CLI
// wiring layer (internal/cli/orchestration_state.go) converts this into a
// coordinator.RetryPolicy. All-zero means "no retry" - the safe default.
//
// Retry is NOT safe for a task with side effects it can't undo. A retry
// re-dispatches the whole task from scratch (a fresh attempt, the same goal
// and tool access) - it does not skip tool calls the failed attempt already
// made. A task that writes a file, calls an external API, or sends a message
// and only THEN hits a late transient error will repeat those side effects
// on retry. Enable this only for tasks whose tool calls are safe to repeat,
// or where that risk is acceptable.
type TaskRetryConfig struct {
	// MaxRetries is the maximum retry attempts per task. 0 (default) disables
	// retry.
	MaxRetries int `toml:"max_retries"`
	// BaseBackoffSeconds is the initial backoff before the first retry.
	BaseBackoffSeconds float64 `toml:"base_backoff_seconds"`
	// MaxBackoffSeconds caps the per-retry backoff.
	MaxBackoffSeconds float64 `toml:"max_backoff_seconds"`
	// BackoffFactor is the exponential multiplier applied after each attempt.
	// 0 selects the coordinator's default (2.0) once MaxRetries > 0.
	BackoffFactor float64 `toml:"backoff_factor"`
	// JitterFraction randomizes each backoff by ±JitterFraction/2. 0 disables
	// jitter.
	JitterFraction float64 `toml:"jitter_fraction"`
}
