package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// workflowRunStatuses is the accepted --status filter set. It mirrors the
// ledger's RunStatus values; an unknown value is rejected rather than
// silently returning nothing.
var workflowRunStatuses = map[string]workflowledger.RunStatus{
	string(workflowledger.RunStatusPending):         workflowledger.RunStatusPending,
	string(workflowledger.RunStatusRunning):         workflowledger.RunStatusRunning,
	string(workflowledger.RunStatusWaitingApproval): workflowledger.RunStatusWaitingApproval,
	string(workflowledger.RunStatusDeliveryPending): workflowledger.RunStatusDeliveryPending,
	string(workflowledger.RunStatusSucceeded):       workflowledger.RunStatusSucceeded,
	string(workflowledger.RunStatusFailed):          workflowledger.RunStatusFailed,
	string(workflowledger.RunStatusCanceled):        workflowledger.RunStatusCanceled,
	string(workflowledger.RunStatusTimedOut):        workflowledger.RunStatusTimedOut,
	string(workflowledger.RunStatusDeliveryFailed):  workflowledger.RunStatusDeliveryFailed,
}

// workflowRunsList is the ledger read seam, overridden in tests to exercise
// the store-failure path.
var workflowRunsList = func(ctx context.Context, repo workflowledger.Repository, filter ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
	return repo.ListRuns(ctx, filter...)
}

// executeWorkflowRuns lists workflow runs newest first, optionally filtered
// by status and bounded by limit.
//
// Without it a run in flight cannot be addressed at all: `workflow run`
// prints run_id only when it finishes, and the worktree name is the
// sanitized (lower-cased) form of the ID, so the ID cannot be read back
// from `worktree list` while the run is still going.
func executeWorkflowRuns(root, configPath, statusFilter string, limit int, stdout, stderr io.Writer) error {
	var filter []workflowledger.RunStatus
	if trimmed := strings.TrimSpace(statusFilter); trimmed != "" {
		status, ok := workflowRunStatuses[trimmed]
		if !ok {
			return fmt.Errorf("workflow runs: unknown status %q (want one of %s)", trimmed, workflowRunStatusNames())
		}
		filter = append(filter, status)
	}
	repo, closeFn, err := openWorkflowReportContext(root, configPath)
	if err != nil {
		return err
	}
	defer closeFn()
	runs, err := workflowRunsList(context.Background(), repo, filter...)
	if err != nil {
		return err
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	if len(runs) == 0 {
		fmt.Fprintln(stdout, "no runs")
		return nil
	}
	for _, r := range runs {
		started := ""
		if !r.StartedAt.IsZero() {
			started = r.StartedAt.UTC().Format(time.RFC3339)
		}
		line := fmt.Sprintf("%s  %-16s %-16s %s", r.RunID, r.WorkflowName, r.Status, started)
		if r.ActiveStepID != "" {
			line += "  step=" + r.ActiveStepID
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// workflowRunStatusNames returns the accepted status values, sorted, for
// error messages.
func workflowRunStatusNames() string {
	names := make([]string, 0, len(workflowRunStatuses))
	for name := range workflowRunStatuses {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
