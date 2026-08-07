package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// executeWorkflowStatus prints a read-only report for one workflow run: the
// run snapshot, numbered attempts, loop counters, approvals, and delivery
// records. It never contacts a provider or mutates the workspace.
func executeWorkflowStatus(runID, root, configPath string, stdout, stderr io.Writer) error {
	repo, closeFn, err := openWorkflowReportContext(root, configPath)
	if err != nil {
		return err
	}
	defer closeFn()
	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, workflowledger.ErrNotFound) {
			return fmt.Errorf("workflow run %q not found", runID)
		}
		return err
	}
	fmt.Fprintf(stdout, "run_id: %s\n", run.RunID)
	fmt.Fprintf(stdout, "workflow: %s (digest %s)\n", run.WorkflowName, shortDigest(run.WorkflowDigest))
	fmt.Fprintf(stdout, "status: %s\n", run.Status)
	fmt.Fprintf(stdout, "active_step: %s\n", run.ActiveStepID)
	if run.BaseRef != "" {
		fmt.Fprintf(stdout, "base: %s @ %s\n", run.BaseRef, shortDigest(run.BaseCommit))
	}
	if run.WorktreeName != "" {
		fmt.Fprintf(stdout, "worktree: %s\n", run.WorktreeName)
	}
	if !run.StartedAt.IsZero() {
		fmt.Fprintf(stdout, "started_at: %s\n", run.StartedAt.UTC().Format(time.RFC3339))
	}
	if run.FinishedAt != nil {
		fmt.Fprintf(stdout, "finished_at: %s\n", run.FinishedAt.UTC().Format(time.RFC3339))
	}

	if err := printWorkflowAttempts(ctx, stdout, repo, runID); err != nil {
		return err
	}
	if err := printWorkflowLoopCounters(ctx, stdout, repo, runID); err != nil {
		return err
	}
	if err := printWorkflowApprovals(ctx, stdout, repo, runID); err != nil {
		return err
	}
	return printWorkflowDeliveries(ctx, stdout, repo, runID)
}

// printWorkflowAttempts prints the run's numbered step attempts.
func printWorkflowAttempts(ctx context.Context, stdout io.Writer, repo workflowledger.Repository, runID string) error {
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "attempts: %d\n", len(attempts))
	for _, a := range attempts {
		line := fmt.Sprintf("  %s #%d %s", a.StepID, a.AttemptNo, a.Status)
		if a.ToStepID != "" {
			line += " -> " + a.ToStepID
		}
		if a.OutputRef != "" {
			line += " output " + a.OutputRef
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// printWorkflowLoopCounters prints the run's loop iteration counters.
func printWorkflowLoopCounters(ctx context.Context, stdout io.Writer, repo workflowledger.Repository, runID string) error {
	counters, err := repo.GetLoopCounters(ctx, runID)
	if err != nil {
		return err
	}
	if len(counters) > 0 {
		fmt.Fprintln(stdout, "loops:")
		for _, c := range counters {
			fmt.Fprintf(stdout, "  %s: %d\n", c.LoopName, c.Iterations)
		}
	}
	return nil
}

// printWorkflowApprovals prints the run's human-gate approvals.
func printWorkflowApprovals(ctx context.Context, stdout io.Writer, repo workflowledger.Repository, runID string) error {
	approvals, err := repo.ListApprovals(ctx, runID)
	if err != nil {
		return err
	}
	if len(approvals) > 0 {
		fmt.Fprintln(stdout, "approvals:")
		for _, a := range approvals {
			line := fmt.Sprintf("  %s %s (step %s)", a.ApprovalID, a.Status, a.StepID)
			if a.Actor != "" {
				line += " by " + a.Actor
			}
			if a.Reason != "" {
				line += " reason: " + a.Reason
			}
			fmt.Fprintln(stdout, line)
		}
	}
	return nil
}

// printWorkflowDeliveries prints the run's delivery records.
func printWorkflowDeliveries(ctx context.Context, stdout io.Writer, repo workflowledger.Repository, runID string) error {
	deliveries, err := repo.ListDeliveries(ctx, runID)
	if err != nil {
		return err
	}
	if len(deliveries) > 0 {
		fmt.Fprintln(stdout, "deliveries:")
		for _, d := range deliveries {
			line := fmt.Sprintf("  %s %s (mode %s, base %s)", d.IdempotencyKey, d.Status, d.Mode, d.BaseRef)
			if d.CommitSHA != "" {
				line += " commit " + shortDigest(d.CommitSHA)
			}
			if d.URL != "" {
				line += " url " + d.URL
			}
			if d.ErrorRef != "" {
				line += " error " + d.ErrorRef
				if body, err := repo.LoadContent(ctx, d.ErrorRef); err == nil && len(body) > 0 {
					line += ": " + deliveryErrorInline(string(body))
				}
			}
			fmt.Fprintln(stdout, line)
		}
	}
	return nil
}

// shortDigest renders a digest prefix for operator output.
func shortDigest(digest string) string {
	if digest == "" {
		return "-"
	}
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// openWorkflowReportContext opens the workspace, config, and workflow store
// for the read-only workflow commands (status, events).
func openWorkflowReportContext(root, configPath string) (workflowledger.Repository, func(), error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, nil, err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	_, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, nil, err
	}
	return repo, closeFn, nil
}
