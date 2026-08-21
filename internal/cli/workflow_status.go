package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/textutil"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// executeWorkflowStatus prints a read-only report for one workflow run: the
// run snapshot, numbered attempts, loop counters, approvals, and delivery
// records. It never contacts a provider or mutates the workspace.
func executeWorkflowStatus(runID, root, configPath string, stdout, stderr io.Writer) error {
	repo, store, resolvedRoot, closeFn, err := openWorkflowReportContextWithStore(root, configPath)
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
	fmt.Fprintf(stdout, "workflow: %s (digest %s)\n", run.WorkflowName, ShortDigest(run.WorkflowDigest))
	fmt.Fprintf(stdout, "status: %s\n", run.Status)
	fmt.Fprintf(stdout, "active_step: %s\n", run.ActiveStepID)
	if run.BaseRef != "" {
		fmt.Fprintf(stdout, "base: %s @ %s\n", run.BaseRef, ShortDigest(run.BaseCommit))
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
	printStackUndrivenNotice(ctx, stdout, resolvedRoot, store, repo, runID, run.Status)

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

// maxAttemptErrorBytes bounds the error text printed under one attempt. A
// failure reason is a short sentence; the cap only stops a pathological
// payload from burying the rest of the report.
const maxAttemptErrorBytes = 2000

// printWorkflowAttempts prints the run's numbered step attempts. A failed
// attempt also prints the stored error text, not only its digest: a digest
// alone makes a failed run undiagnosable from the CLI, which forced reading
// the store out of band to learn why a run stopped.
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
		if !a.StartedAt.IsZero() {
			line += " started=" + a.StartedAt.UTC().Format(time.RFC3339)
		}
		if a.FinishedAt != nil {
			line += " finished=" + a.FinishedAt.UTC().Format(time.RFC3339)
			if !a.StartedAt.IsZero() {
				line += fmt.Sprintf(" elapsed=%ds", int64(a.FinishedAt.Sub(a.StartedAt).Seconds()))
			}
		} else if !a.StartedAt.IsZero() {
			line += fmt.Sprintf(" elapsed=%ds", int64(time.Since(a.StartedAt).Seconds()))
		}
		if a.LastHeartbeatAt.IsZero() {
			line += " last_heartbeat=-"
		} else {
			staleness := int64(time.Since(a.LastHeartbeatAt).Seconds())
			if staleness < 0 {
				staleness = 0
			}
			line += fmt.Sprintf(" last_heartbeat=%s (%ds ago)", a.LastHeartbeatAt.UTC().Format(time.RFC3339), staleness)
		}
		fmt.Fprintln(stdout, line)
		printAttemptError(ctx, stdout, repo, failedAttemptDiagnosticRef(a))
	}
	return nil
}

// failedAttemptDiagnosticRef returns the ref carrying a failed attempt's
// diagnostic, or "" when the attempt did not fail.
//
// The two gate kinds record a failure differently: an agent gate writes the
// reason to ErrorRef, while an evidence gate's verifier output IS the
// diagnostic and lands in OutputRef with ErrorRef empty. Reading only
// ErrorRef left every failed test/verify/preflight gate showing a bare
// digest, which is the case an operator most needs to read.
func failedAttemptDiagnosticRef(a workflowledger.StepAttempt) string {
	if a.Status != workflowledger.AttemptStatusFailed {
		return ""
	}
	if a.ErrorRef != "" {
		return a.ErrorRef
	}
	return a.OutputRef
}

// printAttemptError prints the text behind one attempt's error ref. A ref
// that cannot be loaded degrades to printing the ref, because a status
// report must never fail on a missing or unreadable blob.
func printAttemptError(ctx context.Context, stdout io.Writer, repo workflowledger.Repository, ref string) {
	if ref == "" {
		return
	}
	data, err := repo.LoadContent(ctx, ref)
	if err != nil {
		fmt.Fprintf(stdout, "    error %s (content unavailable: %v)\n", ref, err)
		return
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		fmt.Fprintf(stdout, "    error %s (empty)\n", ref)
		return
	}
	truncated := false
	if len(text) > maxAttemptErrorBytes {
		// Rune-safe cut: a raw byte slice could split a multi-byte rune and
		// emit invalid UTF-8 into the status report (E4, DC-6).
		text = textutil.TruncateRuneSafe(text, maxAttemptErrorBytes)
		truncated = true
	}
	for _, l := range strings.Split(text, "\n") {
		fmt.Fprintf(stdout, "    error: %s\n", l)
	}
	if truncated {
		fmt.Fprintf(stdout, "    error: ... truncated; full text at %s\n", ref)
	}
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
				line += " commit " + ShortDigest(d.CommitSHA)
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

// printStackUndrivenNotice surfaces the exact condition
// classifyStackPlanRunDelivery would refuse to deliver: a DELIVERY_PENDING run
// that is the plan run of a multi-chunk stack whose stack has NOT finished
// driving. Without this, an operator reading `workflow status` sees a plain
// "status: delivery_pending" that looks identical to a normal, healthy
// pending delivery - diagnosing a parked stack required manually reading
// decompose output and cross-checking chunk admission (see the 2026-08-16
// wfr-SHDHU4YMIGNP4GUM incident). The notice must gate on actual completion
// (classifyStackPlanRunDelivery), not merely on the run being a multi-chunk
// plan (stackDecomposedChunks alone): a stack that already drove to
// completion is no longer refused by `workflow deliver` (F11) - printing
// "UNDRIVEN...refuses this run" for it would be actively wrong, telling the
// operator to re-drive a stack that is already done and just awaits its own
// settle/deliver call. The classification runs WITHOUT the remote merge
// oracle (remoteMergeOracle=false): workflow status never contacts git or
// gh, so the display verdict rests on the durable pushed evidence alone. A
// lookup failure inside classifyStackPlanRunDelivery degrades to no notice
// (stackPlanRunNotApplicable), matching its own fail-open behavior.
func printStackUndrivenNotice(ctx context.Context, stdout io.Writer, root string, store *storage.SQLite, repo workflowledger.Repository, runID string, status workflowledger.RunStatus) {
	if status != workflowledger.RunStatusDeliveryPending {
		return
	}
	if classifyStackPlanRunDelivery(ctx, root, store, repo, runID, false) != stackPlanRunIncomplete {
		return
	}
	chunks, _ := stackDecomposedChunks(ctx, repo, runID)
	fmt.Fprintf(stdout, "stack: UNDRIVEN - plan run of a %d-chunk stack parked at delivery_pending with the stack never driven; `mivia workflow deliver` refuses this run. Finish it with `mivia stack drive <workflow> --stack %s`; once the stack completes, `mivia workflow deliver %s` settles the plan run\n", chunks, runID, runID)
}

// ShortDigest renders a digest prefix for operator output.
func ShortDigest(digest string) string {
	if digest == "" {
		return "-"
	}
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}

// OpenWorkflowReportContext opens the workspace, config, and workflow store
// for the read-only workflow commands (status, events).
func OpenWorkflowReportContext(root, configPath string) (workflowledger.Repository, func(), error) {
	repo, _, _, closeFn, err := openWorkflowReportContextWithStore(root, configPath)
	if err != nil {
		return nil, nil, err
	}
	return repo, closeFn, nil
}

// openWorkflowReportContextWithStore is OpenWorkflowReportContext's full
// form: it also returns the underlying task-ledger store and the resolved
// (absolute) workspace root, both needed by report paths that inspect
// stacking state (e.g. printStackUndrivenNotice's completion check), which
// OpenWorkflowReportContext's repo-only callers do not need.
func openWorkflowReportContextWithStore(root, configPath string) (workflowledger.Repository, *storage.SQLite, string, func(), error) {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	work, err := workspace.Open(root)
	if err != nil {
		return nil, nil, "", nil, err
	}
	configPath = workflowConfigPath(work.Abs, configPath)
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		return nil, nil, "", nil, err
	}
	applyPrivacyPolicy(res)
	applyWorkflowStoreRoot(res, work.Abs)
	store, repo, closeFn, err := openWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		return nil, nil, "", nil, err
	}
	return repo, store, work.Abs, closeFn, nil
}
