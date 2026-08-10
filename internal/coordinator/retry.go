// Package coordinator provides the orchestration seam between model-facing
// tools and the subagent execution pool.
package coordinator

import (
	"math"
	"math/rand"
	"time"
)

// RetryPolicy controls the automatic retry behaviour for failed or timed-out
// tasks within a DAG run. Zero values disable retry.
type RetryPolicy struct {
	// MaxRetries is the maximum number of retry attempts per task.
	// 0 means no retry (failed/timed_out tasks are terminal).
	MaxRetries int `toml:"max_retries" json:"max_retries"`

	// BaseBackoff is the initial backoff duration before the first retry.
	// Each subsequent retry multiplies by BackoffFactor, capped at MaxBackoff.
	BaseBackoff time.Duration `toml:"base_backoff" json:"base_backoff"`

	// MaxBackoff caps the per-retry backoff duration.
	MaxBackoff time.Duration `toml:"max_backoff" json:"max_backoff"`

	// BackoffFactor is the multiplier applied to backoff after each attempt.
	// Default 2.0 (exponential). A value of 0 is treated as 2.0.
	BackoffFactor float64 `toml:"backoff_factor" json:"backoff_factor"`

	// JitterFraction adds randomisation: each backoff is multiplied by
	// 1 ± JitterFraction/2. E.g. 0.25 means ±12.5%. 0 disables jitter.
	JitterFraction float64 `toml:"jitter_fraction" json:"jitter_fraction"`
}

// DefaultRetryPolicy is a sensible default: 3 retries, 1s base, 30s cap,
// exponential (2x), 25% jitter.
var DefaultRetryPolicy = RetryPolicy{
	MaxRetries:     3,
	BaseBackoff:    1 * time.Second,
	MaxBackoff:     30 * time.Second,
	BackoffFactor:  2.0,
	JitterFraction: 0.25,
}

// NoRetry is a zero-value policy that disables retry entirely.
var NoRetry = RetryPolicy{}

// IsZero returns true when the policy is zero-valued (no retry).
func (p RetryPolicy) IsZero() bool {
	return p.MaxRetries == 0 && p.BaseBackoff == 0 && p.MaxBackoff == 0 && p.BackoffFactor == 0 && p.JitterFraction == 0
}

// EffectiveBackoff returns the wall-clock delay before the nth retry attempt
// (zero-based: attempt 0 = first retry after initial failure). It applies
// exponential backoff and optional jitter.
func (p RetryPolicy) EffectiveBackoff(attempt int) time.Duration {
	if p.MaxRetries == 0 {
		return 0 // no retry configured
	}
	if attempt < 0 {
		attempt = 0
	}
	factor := p.BackoffFactor
	if factor <= 0 {
		factor = 2.0
	}
	base := p.BaseBackoff
	if base <= 0 {
		base = 100 * time.Millisecond
	}
	// backoff = base * factor^attempt
	backoff := float64(base) * math.Pow(factor, float64(attempt))
	if p.MaxBackoff > 0 && backoff > float64(p.MaxBackoff) {
		backoff = float64(p.MaxBackoff)
	}
	// Apply jitter: multiply by 1 ± JitterFraction/2
	if p.JitterFraction > 0 && p.JitterFraction <= 1.0 {
		jitterRange := p.JitterFraction / 2.0
		// rand.Float64() returns [0.0, 1.0)
		jitter := 1.0 - jitterRange + (rand.Float64() * 2.0 * jitterRange)
		backoff *= jitter
	}
	// Clamp below the int64 conversion boundary. An out-of-range float64
	// converts to a negative time.Duration (MinInt64 on amd64/arm64), so an
	// uncapped policy (MaxBackoff 0) or a jitter-pushed value would schedule
	// the retry in the past: flushRetries re-queues immediately and the pool
	// storms instead of backing off. float64(math.MaxInt64) itself rounds UP
	// to 2^63 and still converts negative, so the clamp sits at the largest
	// float64 strictly below 2^63: 2^63 - 2048 (= math.MaxInt64 - 2047), which
	// converts to a positive in-range time.Duration.
	const maxBackoffFloat = float64(math.MaxInt64 - 2047)
	if backoff > maxBackoffFloat {
		backoff = maxBackoffFloat
	}
	return time.Duration(backoff)
}

// RetryState tracks retry progress for a single task within a run.
// Must be used from a single goroutine (no locking).
type RetryState struct {
	TaskID   string
	Attempts int // number of retries already performed
	Policy   RetryPolicy
	done     chan struct{} // closed when retry budget exhausted or succeeded
}

// NewRetryState creates a RetryState for a task with the given policy.
func NewRetryState(taskID string, policy RetryPolicy) *RetryState {
	return &RetryState{
		TaskID: taskID,
		Policy: policy,
		done:   make(chan struct{}),
	}
}

// CanRetry returns true if more retry attempts are available.
func (rs *RetryState) CanRetry() bool {
	return rs.Attempts < rs.Policy.MaxRetries
}

// NextBackoff returns the delay before the upcoming retry and advances the
// internal attempt counter.
func (rs *RetryState) NextBackoff() time.Duration {
	delay := rs.Policy.EffectiveBackoff(rs.Attempts)
	rs.Attempts++
	return delay
}

// Exhausted marks the retry budget as exhausted (terminal).
func (rs *RetryState) Exhausted() {
	close(rs.done)
}

// Done returns a channel that closes when retries are exhausted.
func (rs *RetryState) Done() <-chan struct{} {
	return rs.done
}
