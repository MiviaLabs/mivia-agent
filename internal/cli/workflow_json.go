package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// readOnlyWorkflowService opens root's workflow ledger and wraps it as an
// workflowledger.Service with no Engine - only the read tools (list/status)
// work; any mutating call refuses via the Service's own nil-Engine guard.
// This is the one-shot machine-readable read path a caller polling on an
// interval (e.g. mivia-agent-desktop; see workflow_progress_bus.go's doc
// comment on why the ledger's own poll cadence, not a cross-process bus,
// is this project's sanctioned way to observe runs from outside the TUI
// process) uses, never to drive them. The returned close func must be
// deferred by the caller.
func readOnlyWorkflowService(root, configPath string) (*workflowledger.Service, func(), error) {
	repo, closeFn, err := OpenWorkflowReportContext(root, configPath)
	if err != nil {
		return nil, nil, err
	}
	svc, err := workflowledger.NewService(workflowledger.ServiceOptions{
		Repo: func(context.Context) (workflowledger.Repository, func(), error) {
			return repo, func() {}, nil
		},
	})
	if err != nil {
		closeFn()
		return nil, nil, err
	}
	return svc, closeFn, nil
}

// executeWorkflowRunsJSON prints workflow_list_runs' own JSON view
// (workflowledger.ListRunsView) to stdout - the one-shot machine-readable
// counterpart of executeWorkflowRuns' human-readable columns, reusing the
// exact same ledger reads (and the same active_step/heartbeat fields a
// desktop-app live run list needs) rather than hand-rolling a second JSON
// shape that could drift from the one the agent tool already returns.
func executeWorkflowRunsJSON(root, configPath, statusFilter string, limit int, stdout io.Writer) error {
	svc, closeFn, err := readOnlyWorkflowService(root, configPath)
	if err != nil {
		return err
	}
	defer closeFn()
	view, err := svc.ListRuns(context.Background(), statusFilter, limit, 0)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(view)
}

// executeWorkflowStatusJSON prints workflow_status' own JSON view
// (workflowledger.StatusView) to stdout for one run - the one-shot
// machine-readable counterpart of executeWorkflowStatus' formatted report,
// carrying every field a run-detail view needs (steps/attempts, verdicts,
// heartbeat and its staleness, delivery/approval records) without a second
// hand-maintained JSON shape.
func executeWorkflowStatusJSON(runID, root, configPath string, stdout io.Writer) error {
	svc, closeFn, err := readOnlyWorkflowService(root, configPath)
	if err != nil {
		return err
	}
	defer closeFn()
	view, err := svc.Status(context.Background(), runID)
	if err != nil {
		return fmt.Errorf("workflow status: %w", err)
	}
	return json.NewEncoder(stdout).Encode(view)
}
