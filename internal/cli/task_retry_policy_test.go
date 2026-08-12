package cli

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
)

// TestTaskRetryPolicyFromConfigDefaultsToNoRetry pins the safe default: an
// unconfigured (all-zero) [subagents.retry] section must convert to
// coordinator.NoRetry, so existing deployments see no behavior change unless
// they explicitly opt in with max_retries > 0.
func TestTaskRetryPolicyFromConfigDefaultsToNoRetry(t *testing.T) {
	got := taskRetryPolicyFromConfig(config.TaskRetryConfig{})
	if got != coordinator.NoRetry {
		t.Fatalf("taskRetryPolicyFromConfig(zero value) = %+v, want coordinator.NoRetry", got)
	}
}

// TestTaskRetryPolicyFromConfigConvertsOptedInValues pins the opt-in
// conversion: an explicit [subagents.retry] section must reach the
// coordinator with matching field values and seconds converted to a
// time.Duration.
func TestTaskRetryPolicyFromConfigConvertsOptedInValues(t *testing.T) {
	cfg := config.TaskRetryConfig{
		MaxRetries:         3,
		BaseBackoffSeconds: 0.5,
		MaxBackoffSeconds:  10,
		BackoffFactor:      2.0,
		JitterFraction:     0.25,
	}
	got := taskRetryPolicyFromConfig(cfg)
	want := coordinator.RetryPolicy{
		MaxRetries:     3,
		BaseBackoff:    500 * time.Millisecond,
		MaxBackoff:     10 * time.Second,
		BackoffFactor:  2.0,
		JitterFraction: 0.25,
	}
	if got != want {
		t.Fatalf("taskRetryPolicyFromConfig(%+v) = %+v, want %+v", cfg, got, want)
	}
}

// TestTaskRetryPolicyFromConfigNegativeMaxRetriesDisables treats a negative
// max_retries the same as zero: NoRetry, not a policy the coordinator would
// reject or misinterpret.
func TestTaskRetryPolicyFromConfigNegativeMaxRetriesDisables(t *testing.T) {
	got := taskRetryPolicyFromConfig(config.TaskRetryConfig{MaxRetries: -1})
	if got != coordinator.NoRetry {
		t.Fatalf("taskRetryPolicyFromConfig(MaxRetries: -1) = %+v, want coordinator.NoRetry", got)
	}
}

// TestTaskRetryPolicyFromConfigZeroBackoffFactorPassesThroughToCoordinatorDefault
// pins that an unset (TOML zero-default) backoff_factor is passed through as
// 0, not silently rewritten here: coordinator.RetryPolicy.EffectiveBackoff
// (internal/coordinator/retry.go) is the single place that substitutes its
// 2.0 default for a non-positive factor, once per backoff computation. Two
// places defaulting the same field independently is how they drift.
func TestTaskRetryPolicyFromConfigZeroBackoffFactorPassesThroughToCoordinatorDefault(t *testing.T) {
	got := taskRetryPolicyFromConfig(config.TaskRetryConfig{MaxRetries: 2})
	if got.BackoffFactor != 0 {
		t.Fatalf("BackoffFactor = %v, want 0 (pass-through; coordinator.RetryPolicy.EffectiveBackoff owns the 2.0 default)", got.BackoffFactor)
	}
	if backoff := got.EffectiveBackoff(0); backoff <= 0 {
		t.Fatalf("EffectiveBackoff(0) = %v, want > 0 (coordinator default must still produce real backoff end-to-end)", backoff)
	}
}
