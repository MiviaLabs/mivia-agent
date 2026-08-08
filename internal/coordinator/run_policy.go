package coordinator

import "github.com/MiviaLabs/mivia-agent/internal/ledger"

func policyWithRetry(policy ledger.RunPolicy, retry RetryPolicy) ledger.RunPolicy {
	if policy.NoRetry {
		policy.RetryMaxRetries = 0
		policy.RetryBaseBackoff = 0
		policy.RetryMaxBackoff = 0
		policy.RetryBackoffFactor = 0
		policy.RetryJitterFraction = 0
		return policy
	}
	if policy.RetryMaxRetries != 0 || policy.RetryBaseBackoff != 0 || policy.RetryMaxBackoff != 0 || policy.RetryBackoffFactor != 0 || policy.RetryJitterFraction != 0 {
		return policy
	}
	policy.RetryMaxRetries = retry.MaxRetries
	policy.RetryBaseBackoff = retry.BaseBackoff
	policy.RetryMaxBackoff = retry.MaxBackoff
	policy.RetryBackoffFactor = retry.BackoffFactor
	policy.RetryJitterFraction = retry.JitterFraction
	return policy
}

func retryPolicyFromRunPolicy(policy ledger.RunPolicy) RetryPolicy {
	if policy.NoRetry {
		return NoRetry
	}
	return RetryPolicy{
		MaxRetries:     policy.RetryMaxRetries,
		BaseBackoff:    policy.RetryBaseBackoff,
		MaxBackoff:     policy.RetryMaxBackoff,
		BackoffFactor:  policy.RetryBackoffFactor,
		JitterFraction: policy.RetryJitterFraction,
	}
}
