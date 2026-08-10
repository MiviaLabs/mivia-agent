// Workflow-run detail modal for the /workflows right sidebar. Enter (or mouse
// double-click) on a sidebar row opens it; the dialog shows the workflow's
// header facts, every compiled definition step in order with its live run
// state (done / active / pending / failed / waiting / canceled / timed_out /
// interrupted), and the run-control actions that actually exist for the run's
// status. Every action routes through an existing fenced engine/tool surface;
// the dialog never mutates run state and never claims a run. The interactive
// half (key handling, rendering, action dispatch) lives in
// workflow_run_dialog_keys.go; this file owns the derived content and the
// async ledger data flow.
package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/agenttools"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/compiler"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// workflowStepState is one step's live run state as shown in the detail
// dialog. States are derived from typed ledger records (StepAttempt,
// ApprovalRecord, run.ActiveStepID); none of it is parsed user input.
type workflowStepState string

const (
	workflowStepDone        workflowStepState = "done"
	workflowStepActive      workflowStepState = "active"
	workflowStepPending     workflowStepState = "pending"
	workflowStepFailed      workflowStepState = "failed"
	workflowStepWaiting     workflowStepState = "waiting"
	workflowStepCanceled    workflowStepState = "canceled"
	workflowStepTimedOut    workflowStepState = "timed_out"
	workflowStepInterrupted workflowStepState = "interrupted"
)

// workflowStepRow is one ordered definition step with its derived live state.
type workflowStepRow struct {
	id     string
	kind   string
	actor  string // agent, verifier, panel summary, or "" (never untrusted text)
	state  workflowStepState
	active bool // step.ID == run.ActiveStepID; drives the visible "here" marker
}

// workflowDialogAction is one run-control action the dialog offers. Every
// action is a wrapper over an existing fenced surface; the dialog never
// mutates run state itself.
type workflowDialogAction struct {
	key         string
	label       string
	confirm     workflowConfirmAction
	needsEngine bool // cancel/resume/deliver/delete route through the engine
}

// workflowRunView is the immutable content snapshot one dialog render is
// based on.
type workflowRunView struct {
	run               workflowledger.RunSnapshot
	header            []string
	notice            string // definition unavailable, run vanished, …
	steps             []workflowStepRow
	actions           []workflowDialogAction
	pendingApprovalID string
}

// buildWorkflowRunView derives the dialog content from one run's typed ledger
// records and the compiled definition. Step states come from typed
// StepAttempt/ApprovalRecord rows and run.ActiveStepID; the definition was
// already parsed and compiled by the existing definition.ParseWorkflowTOML +
// compiler.Compile path (workflowCompiledByName), never re-parsed here. A
// missing definition degrades to header facts plus a notice, never an error;
// an empty run id is the only error (the run vanished before open).
func buildWorkflowRunView(run workflowledger.RunSnapshot, compiled *compiler.CompiledWorkflow, attempts []workflowledger.StepAttempt, approvals []workflowledger.ApprovalRecord, now time.Time) (*workflowRunView, error) {
	if run.RunID == "" {
		return nil, errors.New("workflow run not found")
	}
	v := &workflowRunView{run: run}
	v.header = []string{"workflow: " + run.WorkflowName}
	if compiled != nil && compiled.Description != "" {
		v.header = append(v.header, "description: "+oneLineNotice(compiled.Description))
	}
	v.header = append(v.header, "run: "+run.RunID, "status: "+string(run.Status))
	if !run.StartedAt.IsZero() {
		started := run.StartedAt.Local().Format("2006-01-02 15:04:05")
		elapsed := ""
		if d := now.Sub(run.StartedAt); d >= 0 {
			elapsed = " · elapsed " + formatDuration(d)
		}
		v.header = append(v.header, "started: "+started+elapsed)
	}
	pendingApproval := ""
	for i := range approvals {
		if approvals[i].Status == "pending" {
			pendingApproval = approvals[i].ApprovalID
			break
		}
	}
	v.pendingApprovalID = pendingApproval
	v.actions = workflowRunActions(run.Status, pendingApproval != "")
	if compiled == nil {
		v.notice = "definition unavailable"
		return v, nil
	}
	v.steps = buildWorkflowRunSteps(compiled, run, attempts, approvals)
	return v, nil
}

// buildWorkflowRunSteps maps each compiled step (declaration order) to its
// live state. The latest attempt per step wins; a gate step with a pending
// approval reads waiting; run.ActiveStepID marks the "here" position when no
// attempt row exists yet.
func buildWorkflowRunSteps(compiled *compiler.CompiledWorkflow, run workflowledger.RunSnapshot, attempts []workflowledger.StepAttempt, approvals []workflowledger.ApprovalRecord) []workflowStepRow {
	latest := make(map[string]workflowledger.StepAttempt, len(attempts))
	for i := range attempts {
		a := attempts[i]
		if cur, ok := latest[a.StepID]; !ok || a.StartedAt.After(cur.StartedAt) {
			latest[a.StepID] = a
		}
	}
	rows := make([]workflowStepRow, 0, len(compiled.Steps))
	for _, s := range compiled.Steps {
		var attempt *workflowledger.StepAttempt
		if a, ok := latest[s.ID]; ok {
			attempt = &a
		}
		rows = append(rows, workflowStepRow{
			id: s.ID, kind: s.Kind, actor: workflowStepActorLabel(s),
			state:  stepState(s, run, attempt, approvals),
			active: s.ID == run.ActiveStepID,
		})
	}
	return rows
}

// stepState derives one step's live state. Priority: a pending approval on a
// gate step is waiting; a terminal attempt names its outcome; a running
// attempt and the run's ActiveStepID (no attempt row yet) read active while
// the run is genuinely executing; anything else is pending. The ActiveStepID
// fallback is suppressed while the run is parked at an approval gate, so a
// non-gate step never claims "active" when the run waits on a person.
func stepState(s definition.Step, run workflowledger.RunSnapshot, attempt *workflowledger.StepAttempt, approvals []workflowledger.ApprovalRecord) workflowStepState {
	if isWorkflowGateKind(s.Kind) {
		for i := range approvals {
			if approvals[i].StepID == s.ID && approvals[i].Status == "pending" {
				return workflowStepWaiting
			}
		}
	}
	if attempt != nil {
		switch attempt.Status {
		case workflowledger.AttemptStatusSucceeded:
			return workflowStepDone
		case workflowledger.AttemptStatusFailed:
			return workflowStepFailed
		case workflowledger.AttemptStatusCanceled:
			return workflowStepCanceled
		case workflowledger.AttemptStatusTimedOut:
			return workflowStepTimedOut
		case workflowledger.AttemptStatusInterrupted:
			return workflowStepInterrupted
		default:
			return workflowStepActive
		}
	}
	if run.ActiveStepID == s.ID && !workflowRunParkedForApproval(run, approvals) {
		return workflowStepActive
	}
	return workflowStepPending
}

// workflowRunParkedForApproval reports whether the run is parked at an
// approval gate: a pending ApprovalRecord exists and the ledger status does
// not place the run in flight (pending/running). While parked, no step is
// actively executing, so the ActiveStepID fallback must not read "active".
func workflowRunParkedForApproval(run workflowledger.RunSnapshot, approvals []workflowledger.ApprovalRecord) bool {
	pending := false
	for i := range approvals {
		if approvals[i].Status == "pending" {
			pending = true
			break
		}
	}
	if !pending {
		return false
	}
	return run.Status != workflowledger.RunStatusPending && run.Status != workflowledger.RunStatusRunning
}

// isWorkflowGateKind reports whether the step kind parks a run for a person
// (an approval record can park it at waiting_approval).
func isWorkflowGateKind(kind string) bool {
	return kind == "human_gate" || kind == "agent_gate"
}

// workflowStepActorLabel names the step's executing principal: the agent for
// agent/agent_gate steps, the panel for agent_panel, the verifier (or
// sandboxed program) for evidence_gate, and "human" for human_gate.
func workflowStepActorLabel(s definition.Step) string {
	switch s.Kind {
	case "agent", "agent_gate":
		return s.Agent
	case "agent_panel":
		if s.Panel != nil {
			if len(s.Panel.Members) > 0 {
				return fmt.Sprintf("%d panel agents", len(s.Panel.Members))
			}
			return "panel"
		}
	case "evidence_gate":
		if s.Verifier != "" {
			return s.Verifier
		}
		if s.Command != nil {
			return s.Command.Program
		}
	case "human_gate":
		return "human"
	}
	return ""
}

// workflowRunActions returns the actions valid for one run status. Approve
// and reject additionally require a pending approval record (the availability
// check the plan pins: waiting_approval alone is not enough). Deliver covers
// delivery_failed as the run's only recovery surface (resume refuses it).
func workflowRunActions(status workflowledger.RunStatus, pendingApproval bool) []workflowDialogAction {
	var actions []workflowDialogAction
	if workflowledger.IsResumableRunStatus(status) {
		actions = append(actions,
			workflowDialogAction{key: "c", label: "cancel", confirm: workflowConfirmCancel, needsEngine: true},
			workflowDialogAction{key: "r", label: "resume", confirm: workflowConfirmResume, needsEngine: true},
		)
	}
	if status == workflowledger.RunStatusDeliveryPending || status == workflowledger.RunStatusDeliveryFailed {
		actions = append(actions, workflowDialogAction{key: "d", label: "deliver", confirm: workflowConfirmDeliver, needsEngine: true})
	}
	if status == workflowledger.RunStatusWaitingApproval && pendingApproval {
		actions = append(actions,
			workflowDialogAction{key: "a", label: "approve", confirm: workflowConfirmApprove},
			workflowDialogAction{key: "x", label: "reject", confirm: workflowConfirmReject},
		)
	}
	if workflowledger.IsDeletableRunStatus(status) {
		actions = append(actions, workflowDialogAction{key: "D", label: "delete", confirm: workflowConfirmDelete, needsEngine: true})
	}
	if workflowledger.IsTerminalRunStatus(status) || status == workflowledger.RunStatusDeliveryPending {
		actions = append(actions, workflowDialogAction{key: "u", label: "cleanup", confirm: workflowConfirmCleanup})
	}
	return actions
}

// renderWorkflowStepRow renders one step row: the active marker, the state
// tag, the step id, and the kind with its executing principal.
func renderWorkflowStepRow(s workflowStepRow) string {
	marker := "  "
	if s.active {
		marker = tuiAccentStyle.Render("▶ ")
	}
	state := workflowStepStateStyle(s.state).Render("[" + string(s.state) + "]")
	line := marker + state + " " + s.id
	detail := s.kind
	if s.actor != "" {
		detail += ": " + s.actor
	}
	return line + tuiDimStyle.Render(" · "+detail)
}

func workflowStepStateStyle(state workflowStepState) lipgloss.Style {
	switch state {
	case workflowStepActive:
		return tuiAccentStyle
	case workflowStepFailed, workflowStepCanceled, workflowStepTimedOut:
		return tuiErrorStyle
	case workflowStepWaiting:
		return tuiInfoStyle
	default:
		return tuiDimStyle
	}
}

// workflowConfirmAction identifies one armed run-control confirmation.
type workflowConfirmAction int

const (
	workflowConfirmNone workflowConfirmAction = iota
	workflowConfirmCancel
	workflowConfirmResume
	workflowConfirmDeliver
	workflowConfirmApprove
	workflowConfirmReject
	workflowConfirmDelete
	workflowConfirmCleanup
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
// attempts and approvals, and the compiled definition from the workspace.
type workflowRunDialogData struct {
	run       workflowledger.RunSnapshot
	compiled  *compiler.CompiledWorkflow
	attempts  []workflowledger.StepAttempt
	approvals []workflowledger.ApprovalRecord
}

// workflowRunDialogLoad reads one run's fresh ledger records and its compiled
// definition. The definition lookup reuses workflowCompiledByName (the same
// discover+parse+compile path as the sidebar); a missing or broken definition
// degrades to compiled=nil, never an error. A vanished run returns
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
	return workflowRunDialogData{
		run: run, compiled: workflowCompiledByName(root)[run.WorkflowName],
		attempts: attempts, approvals: approvals,
	}, nil
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
	m.workflowSvc = workflowToolServiceWithBus(root, m.config, func() *events.Bus { return m.eventBus })
	return m.workflowSvc
}
