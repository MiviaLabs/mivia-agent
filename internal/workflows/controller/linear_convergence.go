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
//
// The loop's own max_iterations is the authority on how long a repair may
// run. A loop the author declared unbounded (UnlimitedIterations) is never
// cut short here: a second, hidden bound that overrides the configured one is
// not a stall guard, it is an undocumented cap. On a bounded loop this guard
// only reports the stall earlier than the cap would, with a cause that names
// what happened instead of "loop exhausted".
func (c *LinearController) reviewMadeNoProgress(ctx context.Context, step definition.Step, route RouteDecision, output map[string]any) (bool, error) {
	// Panels count too. The guard used to accept only "agent_gate", which in
	// the shipped feature-delivery workflow matches NOTHING: the sole active
	// reviewer is review_panel, kind "agent_panel". A panel re-emitting the
	// same findings every round therefore burned every iteration and died as
	// "loop exhausted" - exactly the misattributed timeout this guard exists
	// to replace with a named stall.
	if (step.Kind != "agent_gate" && step.Kind != "agent_panel") || route.Loop == "" {
		return false, nil
	}
	if !reviewRequestedChanges(output) {
		return false, nil
	}
	if route.MaxIterations == definition.UnlimitedIterations {
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
	//
	// The window is the maxConvergenceHistory floor, grown so the guard is
	// REACHABLE for every legal budget: one settled round plus identicalLimit-1
	// matched priors must be able to reach identicalRoundLimit, so the window
	// is at least identicalRoundLimit-1. A window hard-capped below the limit
	// would make the stall guard unreachable for large budgets (for example 50
	// identical rounds in a 500-iteration loop), and the loop would exhaust as
	// a misattributed timeout instead of a diagnosed stall.
	repeats := 1 // the round being settled
	window := maxConvergenceHistory
	if limit := identicalRoundLimit(route.MaxIterations); limit-1 > window {
		window = limit - 1
	}
	for _, prior := range priorOutputAttempts(attempts, step.ID, window) {
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
			repeats++
		}
	}
	return repeats >= identicalRoundLimit(route.MaxIterations), nil
}

// identicalRoundLimit returns how many times one findings set may recur
// before a BOUNDED loop is declared stalled. It scales with the budget the
// workflow author configured rather than imposing a fixed number: a loop
// given room to reason gets proportionally more attempts at the same set,
// and a short loop still fails fast.
func identicalRoundLimit(maxIterations int) int {
	limit := maxIterations / 10
	if limit < minIdenticalReviewRounds {
		return minIdenticalReviewRounds
	}
	return limit
}

// minIdenticalReviewRounds is the floor for identicalRoundLimit.
//
// Failing on the FIRST repeat made this a one-strike rule: implement,
// review, implement, review, dead. The implementer got a single attempt at
// any finding set, and a review template that is told to reuse a finding's id
// while it stays open guarantees an identical set whenever one fix lands
// short. That killed runs that were still making progress, which is the
// opposite of what a repair loop is for.
//
// Three occurrences gives two real repair attempts and still stops a genuine
// stall long before the loop cap or the duration bound.
const minIdenticalReviewRounds = 3

// maxConvergenceHistory is the FLOOR of the zero-progress comparison window,
// not a hard cap: one review is always compared against at least this many
// prior rounds, so the per-round work stays bounded on short loops. The window
// grows to identicalRoundLimit(max_iterations)-1 when the configured budget is
// large enough that the floor could not reach the stall threshold (window+1
// must reach the limit), keeping the guard reachable for every legal budget.
// The compiler caps max_iterations at 1000, so the window is at most 99 and
// the check stays linear in the round count; the floor still detects any
// oscillation with a period below it.
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
		// A panel persists a PanelFinalReport, which has no "findings" key at
		// all - its per-finding identity lives in dispositions[].final_finding_id.
		// Without this the set was always empty for a panel, so no two rounds
		// ever compared equal and the guard could not fire even once its kind
		// check allowed panels through.
		return panelFindingIDSet(output)
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

// reviewRequestedChanges reports whether a review output asks for another
// round. An agent_gate states it as output.verdict; a panel states it as the
// PanelFinalReport's host_verdict.
func reviewRequestedChanges(output map[string]any) bool {
	if verdict, _ := output["verdict"].(string); verdict == "changes_requested" {
		return true
	}
	host, _ := output["host_verdict"].(string)
	return host == "changes_requested"
}

// panelFindingIDSet extracts the normalized finding-id set from a
// PanelFinalReport's dispositions. final_finding_id is the synthesized
// identity a later round re-raises; finding_id is the member's own id and is
// used only when the synthesizer left the final id blank.
func panelFindingIDSet(output map[string]any) map[string]bool {
	set := make(map[string]bool)
	items, ok := output["dispositions"].([]any)
	if !ok {
		return set
	}
	for _, item := range items {
		d, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := d["final_finding_id"].(string)
		if id == "" {
			id, _ = d["finding_id"].(string)
		}
		if id != "" {
			set[normalizeFindingID(id)] = true
		}
	}
	return set
}
