package ledger

// ValidTaskTransitions returns true if the transition from oldStatus to
// newStatus is valid per the state model:
//
//	queued -> running
//	queued/running -> cancel_requested -> canceled
//	running -> {completed, failed, timed_out, blocked, retry_pending, awaiting_input}
//	awaiting_input -> {running, cancel_requested, canceled, timed_out, failed}
//	failed/timed_out -> retry_pending -> {queued, canceled}
//	failed/timed_out -> blocked
//	completed, canceled, blocked are terminal
//
// awaiting_input is the first status that may return to running (plan 53.02).
func ValidTaskTransition(oldStatus, newStatus string) bool {
	switch oldStatus {
	case string(TaskStatusQueued):
		return newStatus == string(TaskStatusRunning) ||
			newStatus == string(TaskStatusCancelRequested) ||
			newStatus == string(TaskStatusCanceled) ||
			newStatus == string(TaskStatusBlocked)
	case string(TaskStatusRunning):
		return newStatus == string(TaskStatusCompleted) ||
			newStatus == string(TaskStatusFailed) ||
			newStatus == string(TaskStatusTimedOut) ||
			newStatus == string(TaskStatusCancelRequested) ||
			newStatus == string(TaskStatusCanceled) ||
			newStatus == string(TaskStatusBlocked) ||
			newStatus == string(TaskStatusRetryPending) ||
			newStatus == string(TaskStatusAwaitingInput)
	case string(TaskStatusAwaitingInput):
		// Parked on a question: answer resumes; cancel/timeout/fail terminate.
		return newStatus == string(TaskStatusRunning) ||
			newStatus == string(TaskStatusCancelRequested) ||
			newStatus == string(TaskStatusCanceled) ||
			newStatus == string(TaskStatusTimedOut) ||
			newStatus == string(TaskStatusFailed)
	case string(TaskStatusCancelRequested):
		return newStatus == string(TaskStatusCanceled)
	case string(TaskStatusFailed):
		return newStatus == string(TaskStatusRetryPending) || newStatus == string(TaskStatusBlocked)
	case string(TaskStatusTimedOut):
		return newStatus == string(TaskStatusRetryPending) || newStatus == string(TaskStatusBlocked)
	case string(TaskStatusRetryPending):
		return newStatus == string(TaskStatusQueued) || newStatus == string(TaskStatusCanceled)
	default:
		// completed, canceled, blocked are terminal
		return false
	}
}

// ValidRunTransitions returns true if the transition from oldStatus to
// newStatus is valid for a run.
func ValidRunTransitions(oldStatus, newStatus RunStatus) bool {
	switch oldStatus {
	case RunStatusCreated:
		return newStatus == RunStatusQueued || newStatus == RunStatusCanceled
	case RunStatusQueued:
		return newStatus == RunStatusRunning || newStatus == RunStatusCanceled
	case RunStatusRunning:
		return newStatus == RunStatusCompleted || newStatus == RunStatusFailed || newStatus == RunStatusCanceled
	default:
		// completed, failed, canceled are terminal
		return false
	}
}
