package compiler

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
)

// validateGraph checks reachability, terminal paths, and loop bounds.
func validateGraph(wf *definition.WorkflowFile, stepIDs map[string]bool) error {
	// Check initial step exists
	if !stepIDs[wf.InitialStep] {
		return fmt.Errorf("initial_step %q is not a declared step", wf.InitialStep)
	}

	// Build adjacency list: step -> reachable next steps
	adj := make(map[string][]string)
	for _, t := range wf.Transitions {
		if stepIDs[t.To] {
			adj[t.From] = append(adj[t.From], t.To)
		}
	}

	// BFS reachability from initial_step
	visited := make(map[string]bool)
	queue := []string{wf.InitialStep}
	visited[wf.InitialStep] = true
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
		return fmt.Errorf("unreachable steps: %s", strings.Join(unreachable, ", "))
	}

	// Check that at least one transition leads to a terminal (success or failure)
	hasSuccessPath := false
	for _, t := range wf.Transitions {
		if t.To == "success" {
			hasSuccessPath = true
		}
	}
	// At minimum, success path must exist. Failure path is optional (default on_failure covers it).
	if !hasSuccessPath {
		return fmt.Errorf("no transition leads to the success terminal")
	}

	return nil
}

// validateCycles rejects cycles whose edges are all uncapped unless a global
// limit (max_step_attempts or max_duration_seconds) bounds the overall run.
// A named edge with max_iterations > 0 bounds any cycle it belongs to, so such
// edges are excluded from the uncapped-cycle graph. Reserved terminals
// (success, failure) and unknown targets are not part of the step graph.
func validateCycles(wf *definition.WorkflowFile) error {
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

	return fmt.Errorf("workflow has an unbounded cycle (no loop with max_iterations > 0) but max_step_attempts and max_duration_seconds are both 0; set at least one global limit")
}
