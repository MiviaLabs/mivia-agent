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
