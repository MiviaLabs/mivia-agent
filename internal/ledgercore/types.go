package ledgercore

import "time"

// RunPolicy describes fixed recovery behaviour for one admitted run.
type RunPolicy struct {
	NoRetry         bool `json:"no_retry"`
	FailInterrupted bool `json:"fail_interrupted"`
	// Retry fields are the immutable scheduler policy captured at admission.
	// They are work policy, not caller authority.
	RetryMaxRetries     int           `json:"retry_max_retries"`
	RetryBaseBackoff    time.Duration `json:"retry_base_backoff"`
	RetryMaxBackoff     time.Duration `json:"retry_max_backoff"`
	RetryBackoffFactor  float64       `json:"retry_backoff_factor"`
	RetryJitterFraction float64       `json:"retry_jitter_fraction"`
}
