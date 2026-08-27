package cli

// mivia stack command (plan D2, slice S5): the generic stacking driver CLI.
// Dispatch and the read-only commands (plan, status); drive lives in
// stack_drive.go.

import (
	"bytes"
	"context"
	"fmt"
	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"io"
	"os"
	"sort"
	"strings"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
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
		return clichat.RunStackDrive(args[1:], workspaceRoot, configPath, stdout, stderr)
	case "status":
		return runStackStatus(args[1:], workspaceRoot, configPath, stdout, stderr)
	default:
		return fmt.Errorf("stack: unknown subcommand %q", args[0])
	}
}

// runStackPlan admits a plan-mode run for a stacking-enabled workflow using
// the exact engine admission path the workflow CLI already uses
// (cliworkflow.ExecuteWorkflowRun). A run started WITHOUT stack_mode IS plan mode (step
// 0): the workflow's planning steps plus the engine-injected decompose step
// end with a chunk plan. The plan run id becomes the stack id.
func runStackPlan(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("stack plan: expected one workflow name")
	}
	name := args[0]
	var buf bytes.Buffer
	out := io.MultiWriter(stdout, &buf)
	if err := cliworkflow.ExecuteWorkflowRun(name, workspaceRoot, configPath, nil, false, out, stderr); err != nil {
		return fmt.Errorf("stack plan: %w", err)
	}
	runID, status := parseRunLine(buf.String())
	if runID == "" {
		return fmt.Errorf("stack plan: could not read the plan run id from the run output")
	}
	line, err := stackPlanOutcomeLine(runID, status)
	if err != nil {
		return err
	}
	fmt.Fprint(stdout, line)
	fmt.Fprintf(stdout, "drive the stack with: mivia stack drive %s --stack %s\n", name, runID)
	return nil
}

// stackPlanOutcomeLine reports the status line `runStackPlan` prints for a
// finished plan run, or an error when the run did not reach a state that can
// be driven. A multi-chunk plan under a non-auto merge_policy pauses at
// delivery_pending by design (errStackAwaitsGrant, see
// stack_grant_pause.go): the plan itself succeeded, but the stack awaits its
// first drive. Reporting that as a plan failure misdiagnosed the designed
// pause (F11); a merge_policy=auto stack either finishes here or blocks
// inside cliworkflow.ExecuteWorkflowRun until it does (never returns delivery_pending to
// this point), so seeing delivery_pending here is unambiguously the pause.
func stackPlanOutcomeLine(runID, status string) (string, error) {
	switch status {
	case string(workflowledger.RunStatusSucceeded):
		return fmt.Sprintf("stack=%s plan_run=%s status=%s\n", runID, runID, status), nil
	case string(workflowledger.RunStatusDeliveryPending):
		return fmt.Sprintf("stack=%s plan_run=%s status=%s (awaiting first drive)\n", runID, runID, status), nil
	default:
		return "", fmt.Errorf("stack plan: plan run %s did not succeed (status=%s); fix the plan and re-run", runID, status)
	}
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
	name, stackFlag, rest, err := clichat.ParseStackWorkflowArgsFunc(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("stack status: unexpected argument %q", rest[0])
	}
	ledger, repo, closeFn, err := clichat.OpenStackLedgerFunc(workspaceRoot, configPath)
	if err != nil {
		return err
	}
	defer closeFn()
	stackID, err := clichat.ResolveStackIDFunc(repo, name, stackFlag)
	if err != nil {
		return err
	}
	list, err := ledger.ListTasksByScope(clichat.StackScope(stackID))
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
	for _, line := range clichat.StackGrantHintLines(list, func(chunkID string) string {
		run, found, err := clichat.StackRunRefExport(repo, stackID, chunkID)
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
	run, found, err := clichat.StackRunRefExport(repo, stackID, chunkID)
	if err != nil || !found {
		return "-", "-"
	}
	pr = "-"
	deliveries, err := repo.ListDeliveries(context.Background(), run.RunID)
	if err == nil && len(deliveries) > 0 {
		pr = clichat.StackPRNumber(deliveries[len(deliveries)-1].URL)
		if pr == "" {
			pr = "published"
		}
	}
	return run.RunID, pr
}
