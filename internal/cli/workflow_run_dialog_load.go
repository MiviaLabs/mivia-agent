// Async ledger data flow for the workflow-run detail modal: the fresh-read
// messages, the load path (snapshot-preferred definition compile), the
// throttled refresh command, and the action-outcome message. The derived
// content (step states, action availability) and the interactive half live in
// workflow_run_dialog.go and workflow_run_dialog_keys.go; this file owns only
// how the dialog reads and refreshes its data, never how it renders it.
package cli

import (
	"context"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	tea "github.com/charmbracelet/bubbletea"
)

// workflowRunDialogRefreshMsg carries one fresh ledger read for an open
// workflow-run dialog. The read ran off the update goroutine (tea.Cmd); a
// failed read keeps the previous view and the handler may surface a notice.
type workflowRunDialogRefreshMsg struct {
	runID string
	data  workflowRunDialogData
	err   error
}

// workflowRunDialogData is one fresh ledger read: the run snapshot, its typed
// attempts, approvals, and delivery records, the compiled definition from the
// workspace (preferring the definition the run was admitted with), and the
// run's execution claim for delivery_pending liveness (claimOK=false when the
// run holds no claim or the read failed; a read surface never fails the
// dialog over a claim probe).
type workflowRunDialogData struct {
	run        workflowledger.RunSnapshot
	compiled   *definition.CompiledWorkflow
	attempts   []workflowledger.StepAttempt
	approvals  []workflowledger.ApprovalRecord
	deliveries []workflowledger.DeliveryRecord
	claimAt    time.Time
	claimOK    bool
}

// workflowRunDialogLoad reads one run's fresh ledger records and its compiled
// definition. The definition lookup reuses workflowCompiledByName (the same
// discover+parse+compile path as the sidebar) and prefers the definition the
// run was admitted with: the ledger snapshot freezes the DefinitionTOML, so
// the dialog's step list describes the RUN, never a workspace file that may
// have changed since the run started (the engine's resume and delivery paths
// already compile the snapshot). A missing or broken definition (snapshot or
// workspace) degrades to compiled=nil, never an error. A vanished run returns
// workflowledger.ErrNotFound so the dialog can keep stale content with a
// notice.
func workflowRunDialogLoad(root, configPath, runID string) (workflowRunDialogData, error) {
	repo, closeFn, err := openWorkflowReportContext(root, configPath)
	if err != nil {
		return workflowRunDialogData{}, err
	}
	defer closeFn()
	ctx := context.Background()
	run, err := repo.GetRun(ctx, runID)
	if err != nil {
		return workflowRunDialogData{}, err
	}
	attempts, err := repo.ListStepAttempts(ctx, runID)
	if err != nil {
		return workflowRunDialogData{}, err
	}
	approvals, err := repo.ListApprovals(ctx, runID)
	if err != nil {
		return workflowRunDialogData{}, err
	}
	deliveries, err := repo.ListDeliveries(ctx, runID)
	if err != nil {
		return workflowRunDialogData{}, err
	}
	compiled := workflowCompiledByName(root)[run.WorkflowName]
	if raw, err := repo.GetRunSnapshot(ctx, runID); err == nil {
		if snapshotCompiled := compileWorkflowRunSnapshot(raw, run.WorkflowName); snapshotCompiled != nil {
			compiled = snapshotCompiled
		}
	}
	data := workflowRunDialogData{
		run: run, compiled: compiled,
		attempts: attempts, approvals: approvals, deliveries: deliveries,
	}
	if run.Status == workflowledger.RunStatusDeliveryPending {
		// A fresh claim means a delivery attempt is in flight, a stale one a
		// crashed delivery, and no claim that the run waits for a delivery.
		// The claim probe is read-only and best-effort: a failed read renders
		// as "waiting", never an error.
		if _, at, ok, err := repo.GetRunClaim(ctx, runID); err == nil && ok {
			data.claimAt = at
			data.claimOK = true
		}
	}
	return data, nil
}

// compileWorkflowRunSnapshot compiles the definition a run was admitted with
// (the immutable DefinitionTOML frozen in the ledger snapshot). A nil return
// means the snapshot is unreadable or its definition does not parse; the
// caller then keeps the workspace definition, so a broken snapshot never
// errors the dialog. CompileForResume skips the unbounded-cycle admission
// check: the definition was already admitted, and the dialog is a read
// surface, not an admission gate.
func compileWorkflowRunSnapshot(raw []byte, workflowName string) *definition.CompiledWorkflow {
	snapshot, err := workflowledger.UnmarshalSnapshot(raw)
	if err != nil {
		return nil
	}
	wf, _, err := definition.ParseWorkflowTOML(snapshot.DefinitionTOML, workflowName+".toml")
	if err != nil {
		return nil
	}
	compiled, err := definition.CompileForResume(&wf)
	if err != nil {
		return nil
	}
	return compiled
}

// refreshWorkflowRunDialog returns a tea.Cmd that re-reads one run's ledger
// data off the update goroutine when the dialog is open and the 2s throttle
// window has passed. It mirrors refreshWorkflowsSidebar: a throttled call
// marks the dialog dirty and returns nil, a closed dialog is a no-op, and a
// failed read is carried on the message so the handler keeps the previous
// view.
func (m *tuiModel) refreshWorkflowRunDialog() tea.Cmd {
	dlg := m.workflowRunDlg
	if dlg == nil {
		return nil
	}
	now := time.Now()
	if now.Sub(dlg.lastRefresh) < workflowRunDialogRefreshInterval {
		dlg.dirty = true
		return nil
	}
	dlg.lastRefresh = now
	root := m.resolveRepoRoot()
	configPath := sessionEngineConfigPath(root, m.config)
	runID := dlg.runID
	return func() tea.Msg {
		data, err := workflowRunDialogLoad(root, configPath, runID)
		return workflowRunDialogRefreshMsg{runID: runID, data: data, err: err}
	}
}

// workflowRunDialogActionMsg reports one confirmed action's outcome. err
// carries a refusal (e.g. a live foreign claim) as a notice; a nil err means
// the action settled and the run must be re-read so status, steps, and action
// hints reflect the new state.
type workflowRunDialogActionMsg struct {
	runID  string
	action workflowConfirmAction
	result string
	err    error
}

// openWorkflowRunDialog opens the run-detail modal for one sidebar row. The
// first fresh ledger read runs off the update goroutine as a tea.Cmd
// (delivered via workflowRunDialogRefreshMsg); the returned command must be
// drained by the caller (sidebar key or mouse path). The dialog renders a
// loading placeholder until that read lands.
func (m *tuiModel) openWorkflowRunDialog(row workflowRunRow) tea.Cmd {
	m.closeSuggest()
	m.closeHistory()
	root := m.resolveRepoRoot()
	dlg := &workflowRunDialog{
		runID:      row.run.RunID,
		root:       root,
		configPath: sessionEngineConfigPath(root, m.config),
	}
	if svc := m.workflowDialogService(); svc != nil {
		dlg.engine = svc.Engine()
	}
	m.workflowRunDlg = dlg
	// The dialog owns the screen while open; keep the workflows sidebar as the
	// underlying focus so esc restores it (setFocus falls back to the composer
	// when the sidebar is not visible).
	m.setFocus(focusWorkflowsSidebar)
	m.hitMap.invalidate()
	cmd := m.refreshWorkflowRunDialog()
	m.pendingWorkflowDialogCmd = cmd
	return cmd
}

// takePendingWorkflowDialogCmd drains the async first-read command queued by
// an open path that has no tea.Cmd return of its own (sidebar key handler,
// sidebar mouse). Returns nil when nothing is queued.
func (m *tuiModel) takePendingWorkflowDialogCmd() []tea.Cmd {
	if m.pendingWorkflowDialogCmd == nil {
		return nil
	}
	cmd := m.pendingWorkflowDialogCmd
	m.pendingWorkflowDialogCmd = nil
	return []tea.Cmd{cmd}
}

// workflowDialogService returns the workflow tool service the dialog routes
// actions through, building it once from the session's own config identity
// when the workspace has workflows (same factory and store as the session
// workflow tools). Tests may pre-set m.workflowSvc to a recording service.
func (m *tuiModel) workflowDialogService() *agenttools.Service {
	if m.workflowSvc != nil {
		return m.workflowSvc
	}
	root := m.resolveRepoRoot()
	if root == "" {
		return nil
	}
	m.workflowSvc = workflowToolServiceWithBus(root, m.config, func() *events.Bus { return m.eventBus }, false)
	return m.workflowSvc
}
