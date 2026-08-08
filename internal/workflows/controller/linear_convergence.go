package controller

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// reviewMadeNoProgress reports whether a succeeded agent_gate review with a
// changes_requested verdict reproduced the findings set of ANY recent prior
// round, within maxConvergenceHistory rounds. Both finding-id sets are
// normalized by stripping the leading R<digits>- round prefix, so a review
// that re-emits the SAME findings with a NEW round prefix counts as zero
// progress. The gate applies only to rounds whose findings set is non-empty:
// the first round and any round that reaches a new set route normally on the
// loop back-edge.
func (c *LinearController) reviewMadeNoProgress(ctx context.Context, step definition.Step, route RouteDecision, output map[string]any) (bool, error) {
	if step.Kind != "agent_gate" || route.Loop == "" {
		return false, nil
	}
	if verdict, _ := output["verdict"].(string); verdict != "changes_requested" {
		return false, nil
	}
	current := findingIDSet(output)
	attempts, err := c.Repo.ListStepAttempts(ctx, c.RunID)
	if err != nil {
		return false, err
	}
	// The attempt being settled is still Running with no OutputRef, so every
	// output attempt here is a previous COMPLETED review.
	//
	// The comparison covers a window of prior rounds, not only the last one.
	// Against the immediately prior round alone, a reviewer that alternates
	// between two finding sets (A, B, A, B) never repeats consecutively. Such
	// a run makes no progress but is not detected, so it burns the loop cap
	// and dies at the 24h duration bound as an undiagnosed timeout.
	for _, prior := range priorOutputAttempts(attempts, step.ID, maxConvergenceHistory) {
		raw, err := c.Repo.LoadContent(ctx, prior.OutputRef)
		if err != nil {
			return false, err
		}
		var priorOutput map[string]any
		if err := json.Unmarshal(raw, &priorOutput); err != nil {
			continue
		}
		previous := findingIDSet(priorOutput)
		if len(previous) == 0 {
			continue
		}
		if equalStringSets(previous, current) {
			return true, nil
		}
	}
	return false, nil
}

// maxConvergenceHistory bounds how many prior rounds one review is compared
// against. It caps the work per round: the loop allows up to 500 iterations,
// and an unbounded comparison would make the check quadratic in the round
// count. A window of this size detects any oscillation with a period below it.
const maxConvergenceHistory = 20

// priorOutputAttempts returns up to limit completed attempts of step that
// carry an output ref, most recent first.
func priorOutputAttempts(attempts []workflowledger.StepAttempt, step string, limit int) []workflowledger.StepAttempt {
	matched := make([]workflowledger.StepAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.StepID == step && attempt.OutputRef != "" {
			matched = append(matched, attempt)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].AttemptNo > matched[j].AttemptNo })
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched
}

// failReviewNoProgress degrades a succeeded review that made no progress
// across rounds to a durable failure: the attempt is persisted Failed with
// the zero-progress cause and the on_failure route, so the run stops instead
// of spinning identical findings. The cause is also persisted so the attempt's
// ErrorRef is never empty (storeErrorText stays fail-soft).
func (c *LinearController) failReviewNoProgress(writeCtx context.Context, result *AgentStepResult, step definition.Step, cause error) (workflowledger.AttemptStatus, error, RouteDecision) {
	result.ErrorRef = storeErrorText(writeCtx, c.Repo, cause)
	return workflowledger.AttemptStatusFailed, cause, failureRoute(step)
}

// roundInputForStep returns the synthetic round input for a loop-bound step:
// a step whose own outgoing transition is a named loop back-edge gets
// inputs.round = the loop's durable iteration counter (0 before the first
// back-edge). Review templates use the round to mint stable finding ids
// (R<N>-...). Steps outside a loop omit the input (ok=false). The loop name
// is the one on the step's own back-edge transition, exactly how
// selectRoute/recordLoopAfterComplete know the step's loop.
func (c *LinearController) roundInputForStep(ctx context.Context, step definition.Step) (int, bool, error) {
	loop := c.stepLoopName(step)
	if loop == "" {
		return 0, false, nil
	}
	counters, err := c.Repo.GetLoopCounters(ctx, c.RunID)
	if err != nil {
		return 0, false, err
	}
	round := 0
	for _, lc := range counters {
		if lc.LoopName == loop {
			round = lc.Iterations
			break
		}
	}
	return round, true, nil
}

// roundIDPrefix matches the R<digits>- prefix review templates put on finding
// ids each round (for example R0-abc, R1-def). Normalization strips the prefix
// so identical findings across rounds compare equal.
var roundIDPrefix = regexp.MustCompile(`^R[0-9]+-`)

// normalizeFindingID strips the leading R<digits>- round prefix from a finding
// id. An id without the prefix is returned unchanged.
func normalizeFindingID(id string) string {
	return roundIDPrefix.ReplaceAllString(id, "")
}

// findingIDSet extracts the normalized finding-id set from a review output's
// "findings" field. A finding is either a string id or an object with a
// string "id" field; items without a string id do not participate.
func findingIDSet(output map[string]any) map[string]bool {
	set := make(map[string]bool)
	raw, ok := output["findings"]
	if !ok {
		return set
	}
	items, ok := raw.([]any)
	if !ok {
		return set
	}
	for _, item := range items {
		switch f := item.(type) {
		case map[string]any:
			if id, ok := f["id"].(string); ok && id != "" {
				set[normalizeFindingID(id)] = true
			}
		case string:
			if f != "" {
				set[normalizeFindingID(f)] = true
			}
		}
	}
	return set
}

// equalStringSets compares two sets of strings for membership equality.
func equalStringSets(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for id := range left {
		if !right[id] {
			return false
		}
	}
	return true
}
