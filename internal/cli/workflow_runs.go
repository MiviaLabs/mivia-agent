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
// workflowWatchInterval is how often --watch re-reads the ledger. The reads
// are cheap, but a live run holds its own execution lock and writes attempts,
// so the poll stays well clear of a tight loop.
var workflowWatchInterval = 5 * time.Second

// workflowWatchSleep is a variable so tests drive the loop without waiting.
var workflowWatchSleep = time.Sleep

// executeWorkflowRunsWatch prints one line per run state change and returns
// when every matched run is terminal.
//
// It uses workflowledger.IsTerminalRunStatus rather than its own status list.
// A hand-copied list is how an earlier watcher treated delivery_pending as
// terminal and stopped while a run was still publishing; the ledger owns that
// predicate and documents delivery_pending as a deliberate pause.
func executeWorkflowRunsWatch(root, configPath, statusFilter string, limit int, stdout, stderr io.Writer) error {
	seen := map[string]workflowledger.RunStatus{}
	for {
		runs, err := watchSnapshot(root, configPath, statusFilter, limit)
		if err != nil {
			return err
		}
		pending := 0
		for _, r := range runs {
			if prior, ok := seen[r.RunID]; !ok || prior != r.Status {
				seen[r.RunID] = r.Status
				step := r.ActiveStepID
				if step == "" {
					step = "-"
				}
				fmt.Fprintf(stdout, "%s  %-16s step=%s\n", r.RunID, r.Status, step)
			}
			if !workflowledger.IsTerminalRunStatus(r.Status) {
				pending++
			}
		}
		if pending == 0 {
			return nil
		}
		workflowWatchSleep(workflowWatchInterval)
	}
}

// watchSnapshot reads the same run set executeWorkflowRuns prints, opening and
// closing the store each pass so a watch never pins the ledger open.
func watchSnapshot(root, configPath, statusFilter string, limit int) ([]workflowledger.RunSnapshot, error) {
	var filter []workflowledger.RunStatus
	if trimmed := strings.TrimSpace(statusFilter); trimmed != "" {
		status, ok := workflowRunStatuses[trimmed]
		if !ok {
			return nil, fmt.Errorf("workflow runs: unknown status %q (want one of %s)", trimmed, workflowRunStatusNames())
		}
		filter = append(filter, status)
	}
	repo, closeFn, err := openWorkflowReportContext(root, configPath)
	if err != nil {
		return nil, err
	}
	defer closeFn()
	runs, err := workflowRunsList(context.Background(), repo, filter...)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

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
	ctx := context.Background()
	for _, r := range runs {
		started := ""
		if !r.StartedAt.IsZero() {
			started = r.StartedAt.UTC().Format(time.RFC3339)
		}
		line := fmt.Sprintf("%s  %-16s %-16s %s", r.RunID, r.WorkflowName, r.Status, started)
		if hb := runHeartbeatFreshness(ctx, repo, r); hb != "" {
			line += "  " + hb
		}
		if r.ActiveStepID != "" {
			line += "  step=" + r.ActiveStepID
			if next := workflowNextStep(root, r); next != "" {
				line += "  next=" + next
			}
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// runHeartbeatFreshness renders the last-heartbeat freshness column for one
// run row: "hb <Ns>" when the run's active attempt recorded a heartbeat, or
// "hb -" when the run is in flight but no heartbeat has been recorded yet.
// Terminal runs and read failures carry no column (""), so the listing never
// fails on a missing or locked run.
func runHeartbeatFreshness(ctx context.Context, repo workflowledger.Repository, r workflowledger.RunSnapshot) string {
	if r.ActiveStepID == "" || workflowledger.IsTerminalRunStatus(r.Status) {
		return ""
	}
	attempts, err := repo.ListStepAttempts(ctx, r.RunID)
	if err != nil {
		return ""
	}
	// The active attempt is the newest attempt recorded for the active step
	// (attempts are ordered by event sequence).
	var hb time.Time
	for i := len(attempts) - 1; i >= 0; i-- {
		if attempts[i].StepID == r.ActiveStepID {
			hb = attempts[i].LastHeartbeatAt
			break
		}
	}
	if hb.IsZero() {
		return "hb -"
	}
	d := time.Since(hb)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("hb %ds", int64(d.Seconds()))
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
