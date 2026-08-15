package compiler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// validateGraph checks reachability, terminal paths, and loop bounds.
func validateGraph(wf *definition.WorkflowFile, stepIDs map[string]bool) []string {
	var errs []string

	// Track known-bad step IDs to suppress cascade noise inside this validator.
	badSteps := make(map[string]bool)

	// Check initial step exists
	if !stepIDs[wf.InitialStep] {
		errs = append(errs, fmt.Sprintf("initial_step %q is not a declared step", wf.InitialStep))
		badSteps[wf.InitialStep] = true
	}

	// Build adjacency list: step -> reachable next steps
	adj := make(map[string][]string)
	for _, t := range wf.Transitions {
		if stepIDs[t.To] {
			adj[t.From] = append(adj[t.From], t.To)
		}
		// partial_target is a real route the run can take when a loop
		// exhausts; a step reachable only through it is not an orphan.
		if t.PartialTarget != "" && stepIDs[t.PartialTarget] {
			adj[t.From] = append(adj[t.From], t.PartialTarget)
		}
	}

	// BFS reachability from initial_step (skipped when initial_step is bad to
	// avoid cascade noise: all steps would appear unreachable)
	visited := make(map[string]bool)
	if !badSteps[wf.InitialStep] {
		queue := []string{wf.InitialStep}
		visited[wf.InitialStep] = true

		// Delivery re-entry targets are reachable from the delivery phase, which
		// runs after the success terminal, outside the step graph.
		queue = seedDeliveryTargets(wf, stepIDs, visited, queue)

		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]
			for _, next := range adj[curr] {
				if !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}

		// Check for unreachable steps
		var unreachable []string
		for id := range stepIDs {
			if !visited[id] {
				unreachable = append(unreachable, id)
			}
		}
		if len(unreachable) > 0 {
			sort.Strings(unreachable)
			errs = append(errs, fmt.Sprintf("unreachable steps: %s", strings.Join(unreachable, ", ")))
		}
	}

	// Check that at least one transition leads to a terminal (success or failure)
	hasSuccessPath := false
	for _, t := range wf.Transitions {
		// The matcher is only invoked with real step IDs, so a success edge must
		// originate from a declared step.
		if t.To == "success" && stepIDs[t.From] {
			hasSuccessPath = true
			break
		}
	}
	// At minimum, success path must exist. Failure path is optional (default on_failure covers it).
	if !hasSuccessPath {
		errs = append(errs, "no transition leads to the success terminal")
	}

	return errs
}

// seedDeliveryTargets adds delivery re-entry steps to the reachability
// frontier. The workflow author names them in delivery.on_failure /
// delivery.on_pr_metadata_failure / delivery.on_diff_size_failure; a step only
// those fields reach (the PR-metadata repair step is the shipped example) is
// not a graph orphan. Seeding applies only to an ACTIVE delivery policy (kind
// "pull_request" with a mode other than "" or "none"): only an active policy
// runs after the success terminal and re-enters the graph. An inactive block
// does not run, so a step named only there stays unreachable and
// validateGraph flags it as an orphan. validateDelivery still rejects a
// non-empty on_failure / on_pr_metadata_failure / on_diff_size_failure that
// names no declared step, active or not (a typo is a typo).
func seedDeliveryTargets(wf *definition.WorkflowFile, stepIDs map[string]bool, visited map[string]bool, queue []string) []string {
	if !deliveryActive(wf.Delivery) {
		return queue
	}
	for _, target := range []string{wf.Delivery.OnFailure, wf.Delivery.OnPRMetadataFailure, wf.Delivery.OnDiffSizeFailure} {
		if target != "" && stepIDs[target] && !visited[target] {
			visited[target] = true
			queue = append(queue, target)
		}
	}
	return queue
}

// validateCycles rejects cycles whose edges are all uncapped unless a global
// limit (max_step_attempts or max_duration_seconds) bounds the overall run.
// A named edge with max_iterations > 0 bounds any cycle it belongs to, so such
// edges are excluded from the uncapped-cycle graph. Reserved terminals
// (success, failure) and unknown targets are not part of the step graph.
func validateCycles(wf *definition.WorkflowFile) []string {
	// Build step ID set
	stepIDs := make(map[string]bool, len(wf.Steps))
	for _, s := range wf.Steps {
		stepIDs[s.ID] = true
	}

	// Build adjacency over declared steps only: skip transitions to/from
	// reserved terminals or unknown targets, and skip finite-capped named
	// edges (they bound any cycle they belong to).
	adj := make(map[string][]string, len(stepIDs))
	for _, t := range wf.Transitions {
		if !stepIDs[t.From] || !stepIDs[t.To] {
			continue
		}
		if t.Loop != "" && t.MaxIterations > 0 {
			continue
		}
		adj[t.From] = append(adj[t.From], t.To)
		if t.PartialTarget != "" && stepIDs[t.PartialTarget] {
			adj[t.From] = append(adj[t.From], t.PartialTarget)
		}
	}

	// Detect a cycle with a three-color DFS (white/gray/black).
	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(stepIDs))
	var dfs func(id string) bool
	dfs = func(id string) bool {
		color[id] = gray
		for _, next := range adj[id] {
			switch color[next] {
			case gray:
				return true // back edge -> cycle
			case white:
				if dfs(next) {
					return true
				}
			}
		}
		color[id] = black
		return false
	}

	hasCycle := false
	for id := range stepIDs {
		if color[id] == white && dfs(id) {
			hasCycle = true
			break
		}
	}
	if !hasCycle {
		return nil
	}

	// A global limit bounds the run, so the uncapped cycle is acceptable.
	if wf.Limits.MaxStepAttempts > 0 || wf.Limits.MaxDurationSeconds > 0 {
		return nil
	}

	return []string{"workflow has an unbounded cycle (no loop with max_iterations > 0) but max_step_attempts and max_duration_seconds are both 0; set at least one global limit"}
}
