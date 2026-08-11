package localengine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/textutil"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// runsDir returns the workspace path the harness uses for durable run traces
// (.mivia/runs is gitignored; see .mivia/INDEX.md). The directory is created
// on demand, so a fresh checkout or a workspace without the directory still
// gets a visible local record of every workflow run. The namespace name is
// single-sourced in internal/workspace (namespace.go).
func runsDir(root string) string {
	return workspace.NamespacePath(root, "runs")
}

// ensureRunsDir creates the .mivia/runs directory when it does not exist.
func ensureRunsDir(root string) error {
	return os.MkdirAll(runsDir(root), 0o755)
}

// runSummaryFile is the on-disk JSON summary for one workflow run. It carries
// only bounded metadata and failure hints - never raw prompts or model output.
type runSummaryFile struct {
	RunID        string               `json:"run_id"`
	Workflow     string               `json:"workflow"`
	Status       string               `json:"status"`
	StartedAt    string               `json:"started_at,omitempty"`
	FinishedAt   string               `json:"finished_at,omitempty"`
	BaseRef      string               `json:"base_ref,omitempty"`
	BaseCommit   string               `json:"base_commit,omitempty"`
	Worktree     string               `json:"worktree,omitempty"`
	AttemptCount int                  `json:"attempt_count"`
	Loops        []runSummaryLoop     `json:"loops,omitempty"`
	Deliveries   []runSummaryDelivery `json:"deliveries,omitempty"`
}

type runSummaryLoop struct {
	Name       string `json:"name"`
	Iterations int    `json:"iterations"`
}

type runSummaryDelivery struct {
	Status    string `json:"status"`
	URL       string `json:"url,omitempty"`
	ErrorText string `json:"error_text,omitempty"`
}

// summaryErrorHintMax bounds one resolved delivery failure text.
const summaryErrorHintMax = 4 << 10

// writeRunSummary writes a bounded JSON summary of the run to
// .mivia/runs/<runID>.json, so operators and hosts have a durable local
// record with the failure hints, independent of the sqlite ledger. The write
// is atomic (temp file + rename). It is fail-soft: a trace that cannot be
// written must never fail the run itself, so callers ignore the error.
func writeRunSummary(ctx context.Context, repo workflowledger.Repository, root, runID string) error {
	if root == "" {
		return nil
	}
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	summary := runSummaryFile{
		RunID: runID, Workflow: run.WorkflowName, Status: string(run.Status),
		BaseRef: run.BaseRef, BaseCommit: run.BaseCommit, Worktree: run.WorktreeName,
	}
	if !run.StartedAt.IsZero() {
		summary.StartedAt = run.StartedAt.UTC().Format(time.RFC3339)
	}
	if run.FinishedAt != nil {
		summary.FinishedAt = run.FinishedAt.UTC().Format(time.RFC3339)
	}
	if attempts, err := repo.ListStepAttempts(ctx, runID); err == nil {
		summary.AttemptCount = len(attempts)
	}
	if counters, err := repo.GetLoopCounters(ctx, runID); err == nil {
		for _, c := range counters {
			summary.Loops = append(summary.Loops, runSummaryLoop{Name: c.LoopName, Iterations: c.Iterations})
		}
	}
	if deliveries, err := repo.ListDeliveries(ctx, runID); err == nil {
		for _, d := range deliveries {
			summary.Deliveries = append(summary.Deliveries, runSummaryDelivery{
				Status: d.Status, URL: d.URL, ErrorText: runSummaryErrorText(ctx, repo, d.ErrorRef),
			})
		}
	}
	raw, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureRunsDir(root); err != nil {
		return err
	}
	target := filepath.Join(runsDir(root), runID+".json")
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

// writeRunTrace writes the durable on-disk run summary for runID with a
// bounded background context. Fail-soft by design: a trace that cannot be
// written must never fail the run or the delivery path.
func (e *Engine) writeRunTrace(runID string) {
	if e == nil || e.WorkspaceRoot == "" {
		return
	}
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	_ = writeRunSummary(sctx, e.Repo, e.WorkspaceRoot, runID)
}

// runSummaryErrorText resolves a failed delivery's stored failure text for
// the on-disk summary. Fail-soft: an empty or unresolvable ref yields "".
func runSummaryErrorText(ctx context.Context, repo workflowledger.Repository, ref string) string {
	if ref == "" {
		return ""
	}
	body, err := repo.LoadContent(ctx, ref)
	if err != nil || len(body) == 0 {
		return ""
	}
	return textutil.TruncateRuneSafe(string(body), summaryErrorHintMax)
}
