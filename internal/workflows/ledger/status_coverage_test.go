package ledger

import (
	"testing"
	"time"
)

// TestCoverageAttemptElapsedSecondsZeroStart pins attemptElapsedSeconds on an
// attempt with no start timestamp: it must return 0 without touching the
// clock (covers the zero-start early return).
func TestCoverageAttemptElapsedSecondsZeroStart(t *testing.T) {
	a := StepAttempt{
		AttemptID: "cov-zero-start", RunID: "cov-run", StepID: "one", AttemptNo: 1,
		Status: AttemptStatusRunning,
		// StartedAt intentionally left zero.
	}
	if got := attemptElapsedSeconds(a); got != 0 {
		t.Fatalf("attemptElapsedSeconds(zero StartedAt) = %d, want 0", got)
	}
}

// TestCoverageAttemptElapsedSecondsNegativeSkew pins attemptElapsedSeconds on
// a clock-skewed attempt whose finished timestamp precedes its start: the
// negative duration must clamp to 0 (covers the negative-duration early
// return).
func TestCoverageAttemptElapsedSecondsNegativeSkew(t *testing.T) {
	finished := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	a := StepAttempt{
		AttemptID: "cov-neg-skew", RunID: "cov-run", StepID: "one", AttemptNo: 1,
		Status:     AttemptStatusSucceeded,
		StartedAt:  finished.Add(30 * time.Second),
		FinishedAt: &finished,
	}
	if got := attemptElapsedSeconds(a); got != 0 {
		t.Fatalf("attemptElapsedSeconds(negative duration) = %d, want 0", got)
	}
}

// TestCoverageAttemptElapsedSecondsCompleted pins the positive path so the
// zero-guard branches are verified against a real duration: a completed
// attempt reports whole seconds of finished-minus-started.
func TestCoverageAttemptElapsedSecondsCompleted(t *testing.T) {
	started := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	finished := started.Add(90*time.Second + 400*time.Millisecond)
	a := StepAttempt{
		AttemptID: "cov-completed", RunID: "cov-run", StepID: "one", AttemptNo: 1,
		Status:     AttemptStatusSucceeded,
		StartedAt:  started,
		FinishedAt: &finished,
	}
	if got := attemptElapsedSeconds(a); got != 90 {
		t.Fatalf("attemptElapsedSeconds(completed) = %d, want 90", got)
	}
}
