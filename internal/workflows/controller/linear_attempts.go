package controller

import (
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// latestOutputAttempt returns the latest attempt of step whose OutputRef is
// non-empty. steps.X.output bindings use it so the prior output is always the
// most recent attempt that actually produced an artifact: an in-flight attempt
// (no OutputRef yet) and a failed attempt without output must not shadow a
// prior completed output. Attempt numbering and in-flight detection keep using
// latestAttempt.
func latestOutputAttempt(attempts []workflowledger.StepAttempt, step string) (workflowledger.StepAttempt, bool) {
	var latest workflowledger.StepAttempt
	found := false
	for _, attempt := range attempts {
		if attempt.StepID == step && attempt.OutputRef != "" && (!found || attempt.AttemptNo > latest.AttemptNo) {
			latest, found = attempt, true
		}
	}
	return latest, found
}

func latestAttempt(attempts []workflowledger.StepAttempt, step string) (workflowledger.StepAttempt, bool) {
	var latest workflowledger.StepAttempt
	found := false
	for _, attempt := range attempts {
		if attempt.StepID == step && (!found || attempt.AttemptNo > latest.AttemptNo) {
			latest, found = attempt, true
		}
	}
	return latest, found
}

func nextAttemptNo(attempts []workflowledger.StepAttempt, step string) int {
	latest, ok := latestAttempt(attempts, step)
	if !ok {
		return 1
	}
	return latest.AttemptNo + 1
}
