package controller

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// DefaultMaxTransientRetries bounds how many times one step repeats after a
// transport fault. It applies when the workflow sets no limit of its own.
//
// The number is a stall guard, not a budget the run is meant to spend. A
// provider that is truly down exhausts it in under a minute of waiting, and
// the run then fails with the transport cause named.
const DefaultMaxTransientRetries = 5

// routeFailedAttempt chooses where a failed attempt goes.
//
// A transport fault is not a step outcome: the call never delivered an answer,
// so there is nothing to judge and nothing to repair. Such a failure repeats
// the SAME step. Every other failure - a schema violation, an unusable answer
// - is a real outcome and takes the on_failure route.
func (c *LinearController) routeFailedAttempt(ctx context.Context, step definition.Step, cause error) RouteDecision {
	if c.retryStepAfterTransient(ctx, step, cause) {
		return RouteDecision{ToStepID: step.ID, TransitionIndex: -1}
	}
	return failureRoute(step)
}

// retryStepAfterTransient reports whether this failure earns another try of
// the SAME step, rather than the on_failure route.
//
// A transport fault is not a step outcome. The step asked a question and the
// answer never arrived, so there is nothing to judge, nothing to repair, and
// no reason to end the run. Before this, every failure took one path: a
// network tear routed to the failure terminal exactly like a bad answer, and
// runs lost every finished step because one response body was cut.
//
// Retrying the step is the whole recovery. A repeat is a fresh attempt on the
// same step, so it reuses the attempt machinery, the durable history, and the
// step attempt cap. It never enters a repair loop, because there is nothing to
// repair: the previous try produced no work to fix.
//
// The count is of CONSECUTIVE transport faults on this step. A step that fails
// this way, succeeds, then fails this way again is making progress and starts
// over. Only a step that cannot get an answer at all runs out of tries.
func (c *LinearController) retryStepAfterTransient(ctx context.Context, step definition.Step, cause error) bool {
	if !provider.IsTransient(cause) {
		return false
	}
	limit := c.Workflow.Limits.MaxTransientRetries
	if limit == 0 {
		limit = DefaultMaxTransientRetries
	}
	if limit < 0 {
		return true // the workflow declared transport faults always retryable
	}
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		// The history cannot be read, so the count is unknown. Do not retry on
		// a guess: an unbounded repeat is worse than one honest failure.
		return false
	}
	return consecutiveTransientFailures(ctx, c, attempts, step.ID) < limit
}

// consecutiveTransientFailures counts the run of transport faults at the end
// of this step's history. It stops at the first attempt that was not a
// transport fault, because that attempt is progress.
func consecutiveTransientFailures(ctx context.Context, c *LinearController, attempts []workflowledger.StepAttempt, stepID string) int {
	count := 0
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.StepID != stepID {
			continue
		}
		if a.Status != workflowledger.AttemptStatusFailed {
			return count
		}
		if !attemptFailedOnTransport(ctx, c, a) {
			return count
		}
		count++
	}
	return count
}

// attemptFailedOnTransport reports whether a recorded failure was a transport
// fault. The cause is stored content-addressed, so this reads the text back
// and asks the provider layer to judge it, the same way the live error was
// judged. An unreadable cause counts as NOT transport, so a missing record can
// never grant extra tries.
func attemptFailedOnTransport(ctx context.Context, c *LinearController, a workflowledger.StepAttempt) bool {
	if a.ErrorRef == "" {
		return false
	}
	raw, err := c.Repo.LoadContent(ctx, a.ErrorRef)
	if err != nil {
		return false
	}
	return provider.IsTransient(errorText(string(raw)))
}

// errorText adapts stored text to the error interface so the provider layer
// can judge a recorded cause with the same rules it applies to a live one.
type errorText string

func (e errorText) Error() string { return string(e) }
