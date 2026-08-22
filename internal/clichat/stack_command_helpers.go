package clichat

// stack_command_helpers.go holds the stack command argument, id, and ledger
// helpers moved from internal/cli/stack_command.go; the stack domain lives
// in this package.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/delivery"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// parseStackWorkflowArgs parses `stack <sub> <workflow> [--stack <id>]`,
// returning the workflow name, the optional explicit stack id, and the
// remaining args. The --stack flag pins the stack to one plan run; without
// it the latest plan-mode run of the workflow is used.
func parseStackWorkflowArgs(args []string) (name, stackFlag string, rest []string, err error) {
	stackFlag, rest, _, err = FlagValueFunc(args, "--stack")
	if err != nil {
		return "", "", nil, err
	}
	if len(rest) != 1 {
		if len(rest) == 0 {
			return "", "", nil, fmt.Errorf("stack: expected a workflow name (or --stack <id> with a workflow name)")
		}
		return "", "", nil, fmt.Errorf("stack: unexpected argument %q", rest[0])
	}
	return rest[0], stackFlag, rest[1:], nil
}

// resolveStackID returns the plan run id of the stack: the explicit --stack
// value when given, else the most recent plan-mode run of the workflow (a
// run whose attempts include a succeeded decompose step).
func resolveStackID(repo workflowledger.Repository, workflowName, stackFlag string) (string, error) {
	if strings.TrimSpace(stackFlag) != "" {
		return stackFlag, nil
	}
	runs, err := repo.ListRuns(context.Background())
	if err != nil {
		return "", err
	}
	best := ""
	bestStarted := time.Time{}
	for _, r := range runs {
		if r.WorkflowName != workflowName || r.InvocationKey != "" {
			continue
		}
		if !isStackPlanRun(repo, r) {
			continue
		}
		if best == "" || r.StartedAt.After(bestStarted) {
			best = r.RunID
			bestStarted = r.StartedAt
		}
	}
	if best == "" {
		return "", fmt.Errorf("no plan-mode run found for workflow %q; run `mivia stack plan %s` first", workflowName, workflowName)
	}
	return best, nil
}

// isStackPlanRun reports whether a run is a plan-mode run: it carries a
// succeeded decompose step, the engine-synthesized planning step.
func isStackPlanRun(repo workflowledger.Repository, r workflowledger.RunSnapshot) bool {
	attempts, err := repo.ListStepAttempts(context.Background(), r.RunID)
	if err != nil {
		return false
	}
	for _, a := range attempts {
		if a.StepID == delivery.DecomposeStepID && a.Status == workflowledger.AttemptStatusSucceeded {
			return true
		}
	}
	return false
}

// openStackLedger opens the workspace, config, workflow store, and the task
// ledger (D8: the task ledger is the durable stack state). Non-owning: the
// returned close function closes the shared store.
func openStackLedger(root, configPath string) (*workflowledger.Store, workflowledger.Repository, func(), error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, nil, nil, err
	}
	configPath = cliworkflow.WorkflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, nil, err
	}
	applyPrivacyPolicy(res)
	cliworkflow.ApplyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := cliworkflow.OpenWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, nil, nil, err
	}
	return workflowledger.NewStore(store), repo, closeFn, nil
}
