package cli

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
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

// TestTaskRetryPolicyFromConfigClampsExcessiveMaxRetries is the RED test for
// a hostile-audit finding (security lens): [subagents.retry] had no upper
// bound on max_retries, so a misconfiguration (or a typo like an extra zero)
// could hammer a provider far past what the HTTP-layer retry.go's own
// hardcoded caps allow, with none of that layer's protection. A single task
// retrying maxTaskRetries times is already a lot of provider load; there is
// no legitimate deployment need for an unbounded value.
func TestTaskRetryPolicyFromConfigClampsExcessiveMaxRetries(t *testing.T) {
	got := taskRetryPolicyFromConfig(config.TaskRetryConfig{MaxRetries: 100000})
	if got.MaxRetries != maxTaskRetries {
		t.Fatalf("MaxRetries = %d, want clamped to %d", got.MaxRetries, maxTaskRetries)
	}
}

// TestTaskRetryPolicyFromConfigFloorsExcessivelySmallBaseBackoff is the RED
// test for the same finding: EffectiveBackoff only floors a base of exactly
// zero or negative to 100ms, so a config typo like base_backoff_seconds =
// 0.001 (1ms) sailed through uncapped and could retry-storm a provider almost
// immediately after each failure.
func TestTaskRetryPolicyFromConfigFloorsExcessivelySmallBaseBackoff(t *testing.T) {
	got := taskRetryPolicyFromConfig(config.TaskRetryConfig{MaxRetries: 2, BaseBackoffSeconds: 0.001})
	if got.BaseBackoff != minTaskRetryBaseBackoff {
		t.Fatalf("BaseBackoff = %v, want floored to %v", got.BaseBackoff, minTaskRetryBaseBackoff)
	}
}

// TestInitCoordinatorRetriesTransientFailureFromConfig is a bug-audit
// coverage gap (test-coverage lens): task_retry_config_test.go proves TOML
// parses into TaskRetryConfig, and TestTaskRetryPolicyFromConfig* above prove
// TaskRetryConfig converts into a coordinator.RetryPolicy - but nothing
// chained config.SubagentConfig -> initCoordinator -> an actual coordinator
// run retrying a transient failure, with the backoff fields left at their
// TOML zero-default (only max_retries set), the way a real deployment's
// minimal [subagents.retry] section would look. This closes that seam.
func TestInitCoordinatorRetriesTransientFailureFromConfig(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	attempt := 0
	_ = d.Register(runtime.Subagent, "flaky", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		attempt++
		if attempt == 1 {
			return nil, &provider.TransientError{Err: context.DeadlineExceeded}
		}
		return json.RawMessage(`{"ok":true}`), nil
	}))

	cfg := config.DefaultSubagentConfig
	cfg.TaskRetry = config.TaskRetryConfig{MaxRetries: 2} // backoff fields left at TOML zero-default
	repo := ledger.NewMemoryLedgerRepository()
	c := initCoordinator(d, cfg, repo)

	h, err := c.Spawn(context.Background(), []subagents.Task{{ID: "t1", Name: "flaky"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Join(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if result.Err != nil {
		t.Fatalf("run error = %v, want nil (config-driven retry must recover the transient failure)", result.Err)
	}
	if attempt != 2 {
		t.Fatalf("attempt count = %d, want 2 (config-driven retry must have re-dispatched the task once)", attempt)
	}
}
