package coordinator

import (
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

func retryState(taskID string, states map[string]*RetryState, policy RetryPolicy) *RetryState {
	if state := states[taskID]; state != nil {
		return state
	}
	state := NewRetryState(taskID, policy)
	states[taskID] = state
	return state
}

// shouldRetryTask decides whether a just-finished task gets another attempt.
//
// A "timed_out" task is always eligible (subject to the policy and retry
// budget below): a retry attempt runs under a FRESH per-task timeout, so
// replaying it after its own deadline is still worthwhile. This is why the
// gate below applies only to "failed", not to both terminal statuses -
// provider.IsTransient deliberately reports false for a bare
// context.DeadlineExceeded (retrying under the SAME expired context is
// pointless), which is the right call for that function's actual use - the
// HTTP layer's same-context retry - but wrong here, where the context is
// never reused.
//
// A "failed" task must additionally classify as transient
// (provider.IsTransient) to be eligible: a permanent failure (bad auth, a
// schema violation, a genuine bug in the task) fails identically on every
// attempt, so retrying it only spends budget and delays a result the caller
// could have had immediately.
func (c *coordinator) shouldRetryTask(h *RunHandle, status string, err error, taskID string, states map[string]*RetryState) bool {
	if h.policy().IsZero() || h.policy().MaxRetries <= 0 {
		return false
	}
	switch status {
	case string(ledger.TaskStatusTimedOut):
		// always eligible, see doc comment above.
	case string(ledger.TaskStatusFailed):
		if !provider.IsTransient(err) {
			return false
		}
	default:
		return false
	}
	state := states[taskID]
	return state == nil || state.CanRetry()
}
