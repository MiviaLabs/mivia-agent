package cli

import (
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// workflowNextStep resolves the step that follows a run's active step from
// the workflow definition in the workspace. It returns "" when the run is
// terminal, the workflow name does not resolve, the active step is the last
// declared step, or the definition cannot be read or compiled. It never
// errors: the runs listing must not fail because a definition is missing or
// malformed.
var workflowNextStep = func(root string, run workflowledger.RunSnapshot) string {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}
	if run.WorkflowName == "" || run.ActiveStepID == "" ||
		workflowledger.IsTerminalRunStatus(run.Status) ||
		workflowledger.IsTerminalStepID(run.ActiveStepID) {
		return ""
	}
	workflows, err := definition.DiscoverWorkflows(root)
	if err != nil {
		return ""
	}
	var found *definition.DiscoveredWorkflow
	for i := range workflows {
		if workflows[i].Name == run.WorkflowName {
			found = &workflows[i]
			break
		}
	}
	if found == nil {
		return ""
	}
	wf, _, err := definition.ParseWorkflowTOML(found.Raw, found.Name+".toml")
	if err != nil {
		return ""
	}
	compiled, err := definition.Compile(&wf)
	if err != nil {
		return ""
	}
	return nextStepAfterActive(compiled, run.ActiveStepID)
}

// nextStepAfterActive returns the step declared immediately after activeID in
// the compiled step order, or "" when activeID is not declared or is the last
// step. The reserved terminals "success" and "failure" are never declared
// steps, so they cannot be found here.
func nextStepAfterActive(cw *definition.CompiledWorkflow, activeID string) string {
	if cw == nil {
		return ""
	}
	for i, step := range cw.Steps {
		if step.ID == activeID {
			if i+1 < len(cw.Steps) {
				return cw.Steps[i+1].ID
			}
			return ""
		}
	}
	return ""
}
