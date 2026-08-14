package cli

// mivia stack command (plan D2, slice S5): the generic stacking driver CLI.
// Dispatch and the read-only commands (plan, status); drive lives in
// stack_drive.go.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/tasks"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// runStack dispatches `mivia stack plan|drive|status`.
func runStack(args []string) error {
	return runStackWithIO(args, os.Stdout, os.Stderr)
}

// runStackWithIO parses the shared --workspace/--config flags once and routes
// to the subcommands. Subcommand flag parsing lives with each handler.
func runStackWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("stack: expected plan, drive, or status")
	}
	var workspaceRoot, configPath string
	var err error
	workspaceRoot, args, _, err = flagValue(args, "--workspace")
	if err != nil {
		return err
	}
	configPath, args, _, err = flagValue(args, "--config")
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return fmt.Errorf("stack: expected plan, drive, or status")
	}
	switch args[0] {
	case "plan":
		return runStackPlan(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "drive":
		return runStackDrive(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "status":
		return runStackStatus(args[1:], workspaceRoot, configPath, stdout, stderr)
	default:
		return fmt.Errorf("stack: unknown subcommand %q", args[0])
	}
}

// runStackPlan admits a plan-mode run for a stacking-enabled workflow using
// the exact engine admission path the workflow CLI already uses
// (executeWorkflowRun). A run started WITHOUT stack_mode IS plan mode (step
// 0): the workflow's planning steps plus the engine-injected decompose step
// end with a chunk plan. The plan run id becomes the stack id.
func runStackPlan(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("stack plan: expected one workflow name")
	}
	name := args[0]
	var buf bytes.Buffer
	out := io.MultiWriter(stdout, &buf)
	if err := executeWorkflowRun(name, workspaceRoot, configPath, nil, false, out, stderr); err != nil {
		return fmt.Errorf("stack plan: %w", err)
	}
	runID, status := parseRunLine(buf.String())
	if runID == "" {
		return fmt.Errorf("stack plan: could not read the plan run id from the run output")
	}
	if status != string(workflowledger.RunStatusSucceeded) {
		return fmt.Errorf("stack plan: plan run %s did not succeed (status=%s); fix the plan and re-run", runID, status)
	}
	fmt.Fprintf(stdout, "stack=%s plan_run=%s status=%s\n", runID, runID, status)
	fmt.Fprintf(stdout, "drive the stack with: mivia stack drive %s --stack %s\n", name, runID)
	return nil
}

// parseRunLine extracts the run_id and status values from a workflow run's
// "run_id=... status=..." output. A run may print several run_id lines (the
// immediate post-controller settle and a later delivery/skip settle), so the
// LAST line carrying a run_id wins - the final settled state. It returns ""
// when absent.
func parseRunLine(out string) (runID, status string) {
	for _, line := range strings.Split(out, "\n") {
		var lineRunID, lineStatus string
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "run_id=") {
				lineRunID = strings.TrimPrefix(field, "run_id=")
			}
			if strings.HasPrefix(field, "status=") {
				lineStatus = strings.TrimPrefix(field, "status=")
			}
		}
		if lineRunID != "" {
			runID, status = lineRunID, lineStatus
		}
	}
	return runID, status
}

// runStackStatus prints per-chunk status from the task ledger: chunk id,
// status, run ref, PR number, and depends_on. The run ref and PR number are
// joined from the run ledger by the chunk's stable invocation key (task
// fields are immutable once created; the run ledger is the durable source).
func runStackStatus(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	name, stackFlag, rest, err := parseStackWorkflowArgs(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("stack status: unexpected argument %q", rest[0])
	}
	ledger, repo, closeFn, err := openStackLedger(workspaceRoot, configPath)
	if err != nil {
		return err
	}
	defer closeFn()
	stackID, err := resolveStackID(repo, name, stackFlag)
	if err != nil {
		return err
	}
	list, err := ledger.ListTasksByScope(stackScope(stackID))
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return fmt.Errorf("stack %s has no chunk tasks; run `mivia stack drive %s --stack %s` first", stackID, name, stackID)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	fmt.Fprintf(stdout, "stack: %s\n", stackID)
	fmt.Fprintln(stdout, "chunk\tstatus\trun\tpr\tdepends_on")
	for _, t := range list {
		runRef, pr := stackRunDisplay(repo, stackID, t.ID)
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Status, runRef, pr, strings.Join(t.Deps, ","))
	}
	// Reviewed chunks wait on a human publish grant: print the exact
	// command per chunk so status and the drive's pause guidance agree.
	for _, line := range stackGrantHintLines(list, func(chunkID string) string {
		run, found, err := stackRunRef(repo, stackID, chunkID)
		if err != nil || !found {
			return ""
		}
		return run.RunID
	}) {
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// stackRunDisplay joins a chunk task with its latest run (by invocation key)
// and the run's PR number, for status output.
func stackRunDisplay(repo workflowledger.Repository, stackID, chunkID string) (runRef, pr string) {
	run, found, err := stackRunRef(repo, stackID, chunkID)
	if err != nil || !found {
		return "-", "-"
	}
	pr = "-"
	deliveries, err := repo.ListDeliveries(context.Background(), run.RunID)
	if err == nil && len(deliveries) > 0 {
		pr = stackPRNumber(deliveries[len(deliveries)-1].URL)
		if pr == "" {
			pr = "published"
		}
	}
	return run.RunID, pr
}

// parseStackWorkflowArgs parses `stack <sub> <workflow> [--stack <id>]`,
// returning the workflow name, the optional explicit stack id, and the
// remaining args. The --stack flag pins the stack to one plan run; without
// it the latest plan-mode run of the workflow is used.
func parseStackWorkflowArgs(args []string) (name, stackFlag string, rest []string, err error) {
	stackFlag, rest, _, err = flagValue(args, "--stack")
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
		if a.StepID == stackDecomposeStepID && a.Status == workflowledger.AttemptStatusSucceeded {
			return true
		}
	}
	return false
}

// openStackLedger opens the workspace, config, workflow store, and the task
// ledger (D8: the task ledger is the durable stack state). Non-owning: the
// returned close function closes the shared store.
func openStackLedger(root, configPath string) (*tasks.Store, workflowledger.Repository, func(), error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, nil, nil, err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, nil, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, nil, nil, err
	}
	return tasks.NewStore(store), repo, closeFn, nil
}
