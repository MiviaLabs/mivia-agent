// Interactive half of the workflow-run detail modal: the dialog struct, its
// rendering (frame, pager, footer), keyboard/mouse routing, and the fenced
// action dispatch. The derived content (step states, action availability) and
// the async ledger data flow live in workflow_run_dialog.go.
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// workflowRunDialog is the modal detail view for one workflow run. It reads
// fresh ledger data when it opens and refreshes while open with the same 2s
// throttle as the /workflows sidebar; every action routes through an existing
// fenced engine/tool surface and the dialog itself never claims or writes run
// records.
type workflowRunDialog struct {
	runID       string
	root        string
	configPath  string
	engine      workflowledger.Engine // nil when no engine surface is wired
	view        *workflowRunView
	scroll      int
	confirm     workflowConfirmAction
	notice      string
	noticeErr   bool
	lastRefresh time.Time
	dirty       bool
}

// workflowRunDialogRefreshInterval throttles ledger reads while the dialog is
// open; it shares the sidebar's window so the two surfaces never pile up
// reads in the same tick.
const workflowRunDialogRefreshInterval = workflowsSidebarRefreshInterval

func (d *workflowRunDialog) setNotice(msg string, isErr bool) {
	d.notice = msg
	d.noticeErr = isErr
}

// contentLines is the dialog's semantic content before wrapping and paging.
func (d *workflowRunDialog) contentLines() []string {
	v := d.view
	if v == nil {
		return []string{TUIDimStyle.Render("loading run details…")}
	}
	lines := append([]string(nil), v.header...)
	if v.notice != "" {
		lines = append(lines, tuiErrorStyle.Render(v.notice))
	}
	if len(v.steps) > 0 {
		lines = append(lines, fmt.Sprintf("steps (%d):", len(v.steps)))
		for _, s := range v.steps {
			lines = append(lines, renderWorkflowStepRow(s))
		}
	} else if v.notice == "" {
		lines = append(lines, TUIDimStyle.Render("no steps"))
	}
	return lines
}

func workflowRunDialogPrefs() DialogPrefs {
	return DialogPrefs{PreferredWPct: 78, PreferredHPct: 78, MinW: 40, MinH: 8, FrameCols: 4, FrameRows: 3, Pager: true}
}

func (d *workflowRunDialog) layout(w, h int) DialogLayout {
	return MakeDialogLayout(w, h, workflowRunDialogPrefs(), func(innerW int) (int, int) {
		rows := WrapDisplayRows(d.contentLines(), innerW)
		maxW := 0
		for _, row := range rows {
			maxW = max(maxW, ansi.StringWidth(row))
		}
		return maxW, len(rows)
	})
}

// maxScroll is the largest scroll offset that keeps the pager inside the
// canvas at the given terminal size.
func (d *workflowRunDialog) maxScroll(w, h int) int {
	l := d.layout(max(1, w), max(1, h))
	rows := WrapDisplayRows(d.contentLines(), max(1, l.InnerW))
	return max(0, len(rows)-max(1, l.PageH))
}

func (d *workflowRunDialog) move(delta, w, h int) {
	d.scroll = min(max(0, d.scroll+delta), d.maxScroll(w, h))
}

func (d *workflowRunDialog) clampScroll(w, h int) {
	d.scroll = min(max(0, d.scroll), d.maxScroll(w, h))
}

// ViewAt renders the dialog frame over the paged content rows.
func (d *workflowRunDialog) ViewAt(w, h int) (string, DialogLayout) {
	l := d.layout(max(1, w), max(1, h))
	rows := WrapDisplayRows(d.contentLines(), max(1, l.InnerW))
	d.clampScroll(max(1, w), max(1, h))
	start := min(d.scroll, len(rows))
	end := min(len(rows), start+max(1, l.PageH))
	return renderDialogFrame("◆ workflow run "+d.runID, rows[start:end], d.footer(), l), l
}

// availableActions returns the status-valid actions the dialog can actually
// execute: engine-routed actions are hidden when no engine is wired, so the
// footer never advertises a key that always refuses.
func (d *workflowRunDialog) availableActions() []workflowDialogAction {
	if d.view == nil {
		return nil
	}
	out := make([]workflowDialogAction, 0, len(d.view.actions))
	for _, a := range d.view.actions {
		if a.needsEngine && d.engine == nil {
			continue
		}
		out = append(out, a)
	}
	return out
}

func (d *workflowRunDialog) actionForKey(key string) (workflowDialogAction, bool) {
	for _, a := range d.availableActions() {
		if a.key == key {
			return a, true
		}
	}
	return workflowDialogAction{}, false
}

// footer shows the armed confirmation, the last result/refusal notice, or the
// valid action hints plus scroll/close keys.
func (d *workflowRunDialog) footer() string {
	if d.confirm != workflowConfirmNone {
		return tuiErrorStyle.Render(workflowConfirmPrompt(d.confirm, d.runID)) + TUIDimStyle.Render("  y confirm · n/esc cancel")
	}
	if d.notice != "" {
		style := tuiInfoStyle
		if d.noticeErr {
			style = tuiErrorStyle
		}
		return TUIDimStyle.Render("j/k scroll · esc/q close · ") + style.Render(oneLineNotice(d.notice))
	}
	hints := make([]string, 0, 9)
	for _, a := range d.availableActions() {
		hints = append(hints, a.key+" "+a.label)
	}
	hints = append(hints, "j/k scroll", "esc/q close")
	return TUIDimStyle.Render(strings.Join(hints, " · "))
}

func workflowConfirmPrompt(action workflowConfirmAction, runID string) string {
	label := "confirm"
	switch action {
	case workflowConfirmCancel:
		label = "cancel"
	case workflowConfirmResume:
		label = "resume"
	case workflowConfirmDeliver:
		label = "deliver"
	case workflowConfirmApprove:
		label = "approve"
	case workflowConfirmReject:
		label = "reject"
	case workflowConfirmDelete:
		label = "delete"
	case workflowConfirmCleanup:
		label = "cleanup"
	}
	return fmt.Sprintf("%s run %s?", label, runID)
}

// handleWorkflowRunDialogKey routes modal keys while the run dialog is open:
// esc/q close (restoring the workflows sidebar focus), j/k/up/down page the
// content, and action keys arm a confirmation that y executes or n/esc
// clears. The key never mutates run state; it only arms and dispatches.
func (m *tuiModel) handleWorkflowRunDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.workflowRunDlg
	if d == nil {
		return true, true, nil
	}
	// A notice describes a moment that has passed; the next keystroke is a
	// fresh look (mirrors the effort dialog).
	d.notice = ""
	d.noticeErr = false
	w, h := max(1, m.width), max(1, m.height)
	if d.confirm != workflowConfirmNone {
		switch key {
		case "y":
			return m.executeWorkflowDialogAction()
		case "n", "esc":
			d.confirm = workflowConfirmNone
		}
		return true, true, nil
	}
	switch key {
	case "esc", "q":
		m.closeWorkflowRunDialog()
	case "j", "down":
		d.move(1, w, h)
	case "k", "up":
		d.move(-1, w, h)
	default:
		if action, ok := d.actionForKey(key); ok {
			d.confirm = action.confirm
			return true, true, nil
		}
	}
	return true, true, nil
}

// closeWorkflowRunDialog closes the modal, drains the queued first-read
// command (so a later key/mouse path never dispatches a stale refresh for a
// closed dialog), and restores the workflows sidebar focus (setFocus falls
// back to the composer when the sidebar is gone).
func (m *tuiModel) closeWorkflowRunDialog() {
	m.workflowRunDlg = nil
	m.pendingWorkflowDialogCmd = nil
	m.setFocus(focusWorkflowsSidebar)
	m.hitMap.invalidate()
}

// executeWorkflowDialogAction runs the armed confirmation as an off-goroutine
// tea.Cmd. The confirm is cleared before the command runs; a nil command
// (refused synchronously, e.g. no pending approval) leaves no half-confirmed
// state.
func (m *tuiModel) executeWorkflowDialogAction() (bool, bool, []tea.Cmd) {
	d := m.workflowRunDlg
	if d == nil {
		return true, true, nil
	}
	action := d.confirm
	d.confirm = workflowConfirmNone
	if cmd := m.workflowDialogActionCmd(action); cmd != nil {
		return true, true, []tea.Cmd{cmd}
	}
	return true, true, nil
}

// workflowDialogActionCmd returns the tea.Cmd that runs one confirmed action
// through the existing fenced surface off the update goroutine. A nil return
// means the action cannot run now; the refusal is shown as a notice. Engine
// actions were already filtered by availableActions, so engine is non-nil for
// every branch that reaches it.
func (m *tuiModel) workflowDialogActionCmd(action workflowConfirmAction) tea.Cmd {
	d := m.workflowRunDlg
	if d == nil {
		return nil
	}
	runID, root, configPath := d.runID, d.root, d.configPath
	engine := d.engine
	switch action {
	case workflowConfirmApprove, workflowConfirmReject:
		approvalID := ""
		if d.view != nil {
			approvalID = d.view.pendingApprovalID
		}
		if approvalID == "" {
			d.setNotice("no pending approval for this run", true)
			return nil
		}
		reject := action == workflowConfirmReject
		return func() tea.Msg {
			err := resolveWorkflowDialogApproval(runID, approvalID, root, configPath, workflowApprovalDefaultActor, reject)
			result := "approved"
			if reject {
				result = "rejected"
			}
			return workflowRunDialogActionMsg{runID: runID, action: action, result: result, err: err}
		}
	case workflowConfirmCleanup:
		return func() tea.Msg {
			err := cleanupWorkflowRunForDialog(runID, root, configPath)
			return workflowRunDialogActionMsg{runID: runID, action: action, result: "cleanup done", err: err}
		}
	case workflowConfirmCancel, workflowConfirmResume, workflowConfirmDeliver, workflowConfirmDelete:
		ctx := context.Background()
		switch action {
		case workflowConfirmCancel:
			return func() tea.Msg {
				res, err := engine.Cancel(ctx, runID)
				return workflowRunDialogActionMsg{runID: runID, action: action, result: "canceled · " + res.Status, err: err}
			}
		case workflowConfirmResume:
			return func() tea.Msg {
				res, err := engine.Start(ctx, workflowledger.StartRequest{Resume: true, RunID: runID})
				return workflowRunDialogActionMsg{runID: runID, action: action, result: "resumed · " + res.Status, err: err}
			}
		case workflowConfirmDeliver:
			return func() tea.Msg {
				res, err := engine.Deliver(ctx, runID, true)
				if res.Refused && res.Reason != "" {
					return workflowRunDialogActionMsg{runID: runID, action: action, result: "", err: errors.New(res.Reason)}
				}
				return workflowRunDialogActionMsg{runID: runID, action: action, result: "delivered · " + res.Status, err: err}
			}
		case workflowConfirmDelete:
			return func() tea.Msg {
				res, err := engine.Delete(ctx, runID, false)
				return workflowRunDialogActionMsg{runID: runID, action: action, result: "deleted · " + res.Status, err: err}
			}
		}
	}
	return nil
}
