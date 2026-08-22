package legacytui

import (
	"context"
	"errors"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
	tea "github.com/charmbracelet/bubbletea"
)

// TestWorkflowRunDialogConfirmGating pins the confirm state machine: arming
// does not execute, n clears without executing, and y executes exactly once
// through the engine.
func TestWorkflowRunDialogConfirmGating(t *testing.T) {
	m := newReadyChatModel(40, 100)
	rec := &recordingWorkflowEngine{}
	run := workflowledger.RunSnapshot{RunID: "wfr-CONF1", WorkflowName: "alpha", Status: workflowledger.RunStatusRunning}
	view, err := buildWorkflowRunView(run, nil, nil, nil, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	d := &workflowRunDialog{runID: run.RunID, engine: rec, view: view}
	m.workflowRunDlg = d

	ok, _, cmds := m.handleWorkflowRunDialogKey("c")
	if !ok || d.confirm != workflowConfirmCancel {
		t.Fatalf("arm: ok=%v confirm=%v, want cancel armed", ok, d.confirm)
	}
	if len(cmds) != 0 {
		t.Fatal("arming must not execute anything")
	}

	// n clears without executing.
	ok, _, _ = m.handleWorkflowRunDialogKey("n")
	if !ok || d.confirm != workflowConfirmNone {
		t.Fatalf("n: ok=%v confirm=%v, want cleared", ok, d.confirm)
	}
	if cancels, _, _, _ := rec.called(); cancels != 0 {
		t.Fatal("n executed the action")
	}

	// Re-arm and confirm with y: the engine is called exactly once and the
	// confirm clears.
	_, _, _ = m.handleWorkflowRunDialogKey("c")
	ok, _, cmds = m.handleWorkflowRunDialogKey("y")
	if !ok || len(cmds) != 1 {
		t.Fatalf("y: ok=%v cmds=%d, want one action command", ok, len(cmds))
	}
	if d.confirm != workflowConfirmNone {
		t.Fatal("y left the confirm armed")
	}
	msg := cmds[0]()
	actionMsg, ok := msg.(workflowRunDialogActionMsg)
	if !ok || actionMsg.err != nil {
		t.Fatalf("action command returned %#v", msg)
	}
	_, _ = m.Update(msg)
	if cancels, _, _, _ := rec.called(); cancels != 1 {
		t.Fatalf("cancels = %d, want 1", cancels)
	}
	if d.notice != "canceled · canceled" {
		t.Fatalf("success notice = %q", d.notice)
	}
}

// TestWorkflowRunDialogRefusalClearsConfirmAndShowsNotice pins the refusal
// path: the engine error surfaces as an error notice, the confirm clears, and
// no half-confirmed state survives.
func TestWorkflowRunDialogRefusalClearsConfirmAndShowsNotice(t *testing.T) {
	m := newReadyChatModel(40, 100)
	rec := &recordingWorkflowEngine{err: errors.New("workflow run is claimed by another executor; cancel refused")}
	run := workflowledger.RunSnapshot{RunID: "wfr-REF1", WorkflowName: "alpha", Status: workflowledger.RunStatusRunning}
	view, err := buildWorkflowRunView(run, nil, nil, nil, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	d := &workflowRunDialog{runID: run.RunID, engine: rec, view: view}
	m.workflowRunDlg = d

	m.handleWorkflowRunDialogKey("c")
	_, _, cmds := m.handleWorkflowRunDialogKey("y")
	if len(cmds) != 1 {
		t.Fatalf("cmds = %d, want 1", len(cmds))
	}
	msg := cmds[0]()
	if actionMsg, ok := msg.(workflowRunDialogActionMsg); !ok || actionMsg.err == nil {
		t.Fatalf("refusal not carried on the action message: %#v", msg)
	}
	_, _ = m.Update(msg)
	if d.confirm != workflowConfirmNone {
		t.Fatal("refusal left a half-confirmed state")
	}
	if d.noticeErr != true || !strings.Contains(d.notice, "cancel refused") {
		t.Fatalf("refusal notice = %q err=%v", d.notice, d.noticeErr)
	}
}

// TestWorkflowRunDialogActionKeysRouteToEngine drives every engine-routed
// action key per status and asserts the recording engine saw the call.
func TestWorkflowRunDialogActionKeysRouteToEngine(t *testing.T) {
	cases := []struct {
		name    string
		status  workflowledger.RunStatus
		key     string
		confirm workflowConfirmAction
		check   func(t *testing.T, rec *recordingWorkflowEngine)
	}{
		{"cancel", workflowledger.RunStatusRunning, "c", workflowConfirmCancel, func(t *testing.T, rec *recordingWorkflowEngine) {
			if c, _, _, _ := rec.called(); c != 1 {
				t.Fatalf("cancels = %d, want 1", c)
			}
		}},
		{"resume", workflowledger.RunStatusPending, "r", workflowConfirmResume, func(t *testing.T, rec *recordingWorkflowEngine) {
			if _, r, _, _ := rec.called(); r != 1 {
				t.Fatalf("resumes = %d, want 1", r)
			}
		}},
		{"deliver", workflowledger.RunStatusDeliveryPending, "d", workflowConfirmDeliver, func(t *testing.T, rec *recordingWorkflowEngine) {
			if _, _, d, _ := rec.called(); d != 1 {
				t.Fatalf("delivers = %d, want 1", d)
			}
		}},
		{"delete", workflowledger.RunStatusSucceeded, "D", workflowConfirmDelete, func(t *testing.T, rec *recordingWorkflowEngine) {
			if _, _, _, del := rec.called(); del != 1 {
				t.Fatalf("deletes = %d, want 1", del)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newReadyChatModel(40, 100)
			rec := &recordingWorkflowEngine{}
			run := workflowledger.RunSnapshot{RunID: "wfr-KEY1", WorkflowName: "alpha", Status: tc.status}
			view, err := buildWorkflowRunView(run, nil, nil, nil, time.Now(), workflowRunDeliveryClaim{})
			if err != nil {
				t.Fatal(err)
			}
			d := &workflowRunDialog{runID: run.RunID, engine: rec, view: view}
			m.workflowRunDlg = d

			ok, _, _ := m.handleWorkflowRunDialogKey(tc.key)
			if !ok || d.confirm != tc.confirm {
				t.Fatalf("%q armed confirm %v, want %v", tc.key, d.confirm, tc.confirm)
			}
			_, _, cmds := m.handleWorkflowRunDialogKey("y")
			if len(cmds) != 1 {
				t.Fatalf("cmds = %d, want 1", len(cmds))
			}
			actionMsg, ok := cmds[0]().(workflowRunDialogActionMsg)
			if !ok {
				t.Fatalf("action command returned %T", cmds[0]())
			}
			if actionMsg.err != nil {
				t.Fatalf("action error = %v", actionMsg.err)
			}
			_, _ = m.Update(actionMsg)
			tc.check(t, rec)
		})
	}
}

// TestWorkflowRunDialogRefreshThrottle pins that refreshWorkflowRunDialog
// returns an off-goroutine tea.Cmd delivering a workflowRunDialogRefreshMsg,
// that a throttled call returns nil and marks the dialog dirty, and that a
// closed dialog issues nothing.
func TestWorkflowRunDialogRefreshThrottle(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-DLGASYNC1")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	m := newReadyChatModel(40, 100)
	m.workspaceDir = root
	res, err := config.Load(config.LoadOptions{ConfigPath: filepath.Join(root, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	m.config = res
	m.workflowRunDlg = &workflowRunDialog{runID: "wfr-DLGASYNC1"}

	cmd := m.refreshWorkflowRunDialog()
	if cmd == nil {
		t.Fatal("open unthrottled dialog returned no refresh command")
	}
	msg := cmd()
	refresh, ok := msg.(workflowRunDialogRefreshMsg)
	if !ok {
		t.Fatalf("refresh command returned %T, want workflowRunDialogRefreshMsg", msg)
	}
	if refresh.err != nil {
		t.Fatalf("refresh err = %v, want nil", refresh.err)
	}
	if refresh.data.run.RunID != "wfr-DLGASYNC1" {
		t.Fatalf("refresh run = %s, want the seeded run", refresh.data.run.RunID)
	}
	if refresh.data.compiled == nil {
		t.Fatal("refresh did not resolve the compiled definition")
	}

	if cmd := m.refreshWorkflowRunDialog(); cmd != nil {
		t.Fatal("throttled refresh returned a command")
	}
	if !m.workflowRunDlg.dirty {
		t.Fatal("throttled refresh did not mark the dialog dirty")
	}

	m.workflowRunDlg = nil
	if cmd := m.refreshWorkflowRunDialog(); cmd != nil {
		t.Fatal("closed dialog issued a refresh command")
	}
}

// TestWorkflowRunDialogLoadVanishedRun pins the negative path: a run that
// disappeared between the sidebar list and the dialog open surfaces
// ErrNotFound so the dialog can keep stale content with a notice.
func TestWorkflowRunDialogLoadVanishedRun(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-VANISH1")
	defer closeFn()
	if _, err := workflowRunDialogLoad(root, filepath.Join(root, "config.toml"), "wfr-NOPE"); !errors.Is(err, workflowledger.ErrNotFound) {
		t.Fatalf("load err = %v, want ErrNotFound", err)
	}
}

// TestWorkflowRunDialogRefreshMsgAppliesAndKeepsStale pins the refresh handler:
// fresh data builds the view, a vanished run keeps the previous view with a
// notice, a generic read failure keeps the previous view, and a closed dialog
// ignores the message.
func TestWorkflowRunDialogRefreshMsgAppliesAndKeepsStale(t *testing.T) {
	m := newReadyChatModel(40, 100)
	m.workflowRunDlg = &workflowRunDialog{runID: "wfr-APP2"}

	fresh := workflowRunDialogData{run: workflowledger.RunSnapshot{RunID: "wfr-APP2", WorkflowName: "alpha", Status: workflowledger.RunStatusRunning}}
	_, _ = m.Update(workflowRunDialogRefreshMsg{runID: "wfr-APP2", data: fresh})
	if m.workflowRunDlg.view == nil || m.workflowRunDlg.view.run.RunID != "wfr-APP2" {
		t.Fatalf("fresh refresh did not build the view: %#v", m.workflowRunDlg.view)
	}

	_, _ = m.Update(workflowRunDialogRefreshMsg{runID: "wfr-APP2", err: workflowledger.ErrNotFound})
	if m.workflowRunDlg.notice != "run no longer exists" {
		t.Fatalf("vanished-run notice = %q", m.workflowRunDlg.notice)
	}
	if m.workflowRunDlg.view == nil {
		t.Fatal("vanished run dropped the stale view")
	}

	m.workflowRunDlg.setNotice("", false)
	_, _ = m.Update(workflowRunDialogRefreshMsg{runID: "wfr-APP2", err: errors.New("read failed")})
	if m.workflowRunDlg.view == nil {
		t.Fatal("generic read failure dropped the previous view")
	}
	if m.workflowRunDlg.notice != "" {
		t.Fatalf("generic read failure set a notice: %q", m.workflowRunDlg.notice)
	}

	m.workflowRunDlg = nil
	_, _ = m.Update(workflowRunDialogRefreshMsg{runID: "wfr-APP2", data: fresh})
}

// TestWorkflowRunDialogActionMsgDeleteClosesDialog pins that a successful
// delete closes the dialog (the run's durable record is gone) and refreshes
// the sidebar so the row disappears.
func TestWorkflowRunDialogActionMsgDeleteClosesDialog(t *testing.T) {
	m := newReadyChatModel(40, 100)
	m.workflowsSidebar = newWorkflowsSidebar()
	m.workflowRunDlg = &workflowRunDialog{runID: "wfr-DEL1"}
	_, _ = m.Update(workflowRunDialogActionMsg{runID: "wfr-DEL1", action: workflowConfirmDelete, result: "deleted · canceled"})
	if m.workflowRunDlg != nil {
		t.Fatal("successful delete did not close the dialog")
	}
	if m.focus != cli.FocusWorkflowsSidebar {
		t.Fatalf("focus after delete = %v, want workflows sidebar", m.focus)
	}
	if m.workflowsSidebar == nil {
		t.Fatal("delete closed the sidebar")
	}
}

// TestWorkflowRunDialogLoadCarriesDeliveryClaim pins that the dialog load
// reads the run's execution claim for a delivery_pending run (fresh claim =
// delivery in flight) and leaves claimOK=false when no claim is held. The
// claim probe is read-only: it never disturbs the held claim.
func TestWorkflowRunDialogLoadCarriesDeliveryClaim(t *testing.T) {
	root, _, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-DLVC1")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	stored, err := repo.GetRun(ctx, "wfr-DLVC1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-DLVC1", stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	running, err := repo.GetRun(ctx, "wfr-DLVC1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-DLVC1", running.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	if err := repo.ClaimRun(ctx, "wfr-DLVC1", "wfdel-abc"); err != nil {
		t.Fatal(err)
	}
	data, err := workflowRunDialogLoad(root, configPath, "wfr-DLVC1")
	if err != nil {
		t.Fatal(err)
	}
	if !data.claimOK {
		t.Fatalf("claimOK = false, want true for a claimed delivery_pending run")
	}
	if data.claimAt.IsZero() {
		t.Fatal("claimAt is zero")
	}
	// The probe must not have disturbed the claim: a second holder still fails.
	if err := repo.ClaimRun(ctx, "wfr-DLVC1", "wfdel-other"); err == nil {
		t.Fatal("claim probe disturbed the held claim")
	}

	// Without a claim, the same load reads claimOK=false (waiting for delivery).
	if err := repo.ClearRunClaim(ctx, "wfr-DLVC1"); err != nil {
		t.Fatal(err)
	}
	data, err = workflowRunDialogLoad(root, configPath, "wfr-DLVC1")
	if err != nil {
		t.Fatal(err)
	}
	if data.claimOK {
		t.Fatal("claimOK = true after the claim was released")
	}
}

// TestWorkflowRunDialogApproveActionResolvesApproval drives the approve action
// end-to-end against a real ledger: the pending human-gate approval resolves
// through the bounded controller path and the run is re-readable afterwards.
func TestWorkflowRunDialogApproveActionResolvesApproval(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	m := newReadyChatModel(40, 100)
	m.workspaceDir = root
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	m.config = res

	data, err := workflowRunDialogLoad(root, configPath, runID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := buildWorkflowRunView(data.run, data.compiled, data.attempts, data.approvals, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	if view.pendingApprovalID != "wfa-approval-review-1" {
		t.Fatalf("pendingApprovalID = %q, want wfa-approval-review-1", view.pendingApprovalID)
	}
	if len(view.steps) != 2 || view.steps[0].state != workflowStepDone || view.steps[1].state != workflowStepWaiting {
		t.Fatalf("view steps = %#v, want one done and the gate waiting", view.steps)
	}

	d := &workflowRunDialog{runID: runID, root: root, configPath: configPath, view: view}
	m.workflowRunDlg = d
	if _, ok := d.actionForKey("a"); !ok {
		t.Fatal("approve must be offered for a waiting_approval run with a pending approval")
	}
	ok, _, _ := m.handleWorkflowRunDialogKey("a")
	if !ok || d.confirm != workflowConfirmApprove {
		t.Fatalf("approve key did not arm the confirmation (confirm=%v)", d.confirm)
	}
	_, _, cmds := m.handleWorkflowRunDialogKey("y")
	if len(cmds) != 1 {
		t.Fatalf("cmds = %d, want 1", len(cmds))
	}
	actionMsg, ok := cmds[0]().(workflowRunDialogActionMsg)
	if !ok {
		t.Fatalf("approve command returned %T", cmds[0]())
	}
	if actionMsg.err != nil {
		t.Fatalf("approve error = %v", actionMsg.err)
	}

	repo := openWorkflowTestStore(t, storePath)
	approvals, err := repo.ListApprovals(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].ApprovalID != "wfa-approval-review-1" || approvals[0].Status != "approved" || approvals[0].Actor != cliworkflow.WorkflowApprovalDefaultActor {
		t.Fatalf("approvals = %#v, want one approved by %s", approvals, cliworkflow.WorkflowApprovalDefaultActor)
	}
}

// TestWorkflowRunDialogRejectActionSettlesRun drives the reject action against
// the same fixture: rejection resolves the approval to rejected (the run
// settles through the controller's rejection path).
func TestWorkflowRunDialogRejectActionSettlesRun(t *testing.T) {
	root, configPath, storePath, runID := newGatedApprovalFixture(t)
	m := newReadyChatModel(40, 100)
	m.workspaceDir = root
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	m.config = res

	data, err := workflowRunDialogLoad(root, configPath, runID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := buildWorkflowRunView(data.run, data.compiled, data.attempts, data.approvals, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	d := &workflowRunDialog{runID: runID, root: root, configPath: configPath, view: view}
	m.workflowRunDlg = d
	if _, ok := d.actionForKey("x"); !ok {
		t.Fatal("reject must be offered for a waiting_approval run with a pending approval")
	}
	_, _, cmds := m.handleWorkflowRunDialogKey("x")
	_, _, cmds = m.handleWorkflowRunDialogKey("y")
	if len(cmds) != 1 {
		t.Fatalf("cmds = %d, want 1", len(cmds))
	}
	actionMsg, ok := cmds[0]().(workflowRunDialogActionMsg)
	if !ok || actionMsg.err != nil {
		t.Fatalf("reject command = %#v, want success", actionMsg)
	}

	repo := openWorkflowTestStore(t, storePath)
	approvals, err := repo.ListApprovals(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].Status != "rejected" {
		t.Fatalf("approvals = %#v, want one rejected record", approvals)
	}
}

// TestWorkflowRunDialogCloseDrainsPendingCmd pins that closing the dialog
// leaves no pending first-read command queued for the next key/mouse path.
func TestWorkflowRunDialogCloseDrainsPendingCmd(t *testing.T) {
	m := newReadyChatModel(40, 100)
	m.workflowRunDlg = &workflowRunDialog{runID: "wfr-DRAIN1"}
	m.pendingWorkflowDialogCmd = func() tea.Msg { return workflowRunDialogRefreshMsg{runID: "wfr-DRAIN1"} }
	m.closeWorkflowRunDialog()
	if m.workflowRunDlg != nil {
		t.Fatal("closeWorkflowRunDialog did not close the dialog")
	}
	if cmds := m.takePendingWorkflowDialogCmd(); len(cmds) != 0 {
		t.Fatal("closing the dialog must drain the pending command")
	}
}

// TestWorkflowRunDialogHidesEngineActionsWithoutEngine pins that engine-routed
// actions are hidden (not offered) when no engine surface is wired, so the
// footer never advertises a key that always refuses.
func TestWorkflowRunDialogHidesEngineActionsWithoutEngine(t *testing.T) {
	run := workflowledger.RunSnapshot{RunID: "wfr-NOENG1", WorkflowName: "alpha", Status: workflowledger.RunStatusRunning}
	view, err := buildWorkflowRunView(run, nil, nil, nil, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	d := &workflowRunDialog{runID: run.RunID, view: view} // engine is nil
	footer := cli.StripANSI(d.footer())
	if strings.Contains(footer, "c cancel") || strings.Contains(footer, "r resume") {
		t.Fatalf("engine actions must be hidden without an engine:\n%s", footer)
	}
	if _, ok := d.actionForKey("c"); ok {
		t.Fatal("cancel must not be offered without an engine")
	}
}

// TestWorkflowRunDialogApproveWithoutPendingApprovalRefuses pins the nil-cmd
// refusal path: an approve confirmation with no pending approval ID never
// dispatches and leaves no half-confirmed state.
func TestWorkflowRunDialogApproveWithoutPendingApprovalRefuses(t *testing.T) {
	m := newReadyChatModel(40, 100)
	run := workflowledger.RunSnapshot{RunID: "wfr-APPNONE1", WorkflowName: "alpha", Status: workflowledger.RunStatusWaitingApproval}
	view, err := buildWorkflowRunView(run, nil, nil, nil, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	d := &workflowRunDialog{runID: run.RunID, view: view}
	m.workflowRunDlg = d
	d.confirm = workflowConfirmApprove // a refresh may have stripped the approval
	_, _, cmds := m.handleWorkflowRunDialogKey("y")
	if len(cmds) != 0 {
		t.Fatal("approve without a pending approval must not dispatch")
	}
	if d.confirm != workflowConfirmNone || d.noticeErr != true || !strings.Contains(d.notice, "no pending approval") {
		t.Fatalf("confirm=%v notice=%q err=%v, want cleared refusal", d.confirm, d.notice, d.noticeErr)
	}
}

// TestWorkflowRunDialogCleanupActionRefusesActiveRun drives the cleanup action
// command against the real surface: cleanup on an active run refuses through
// executeWorkflowCleanup's status gate and surfaces as an error notice.
func TestWorkflowRunDialogCleanupActionRefusesActiveRun(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-CLEAN1")
	defer closeFn()
	m := newReadyChatModel(40, 100)
	m.workspaceDir = root
	res, err := config.Load(config.LoadOptions{ConfigPath: filepath.Join(root, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	m.config = res
	run := workflowledger.RunSnapshot{RunID: "wfr-CLEAN1", WorkflowName: "alpha", Status: workflowledger.RunStatusSucceeded}
	view, err := buildWorkflowRunView(run, nil, nil, nil, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	d := &workflowRunDialog{runID: "wfr-CLEAN1", root: root, configPath: filepath.Join(root, "config.toml"), view: view}
	m.workflowRunDlg = d
	if _, ok := d.actionForKey("u"); !ok {
		t.Fatal("cleanup must be offered for a succeeded run")
	}
	_, _, cmds := m.handleWorkflowRunDialogKey("u")
	if d.confirm != workflowConfirmCleanup {
		t.Fatalf("confirm = %v, want cleanup armed", d.confirm)
	}
	_, _, cmds = m.handleWorkflowRunDialogKey("y")
	if len(cmds) != 1 {
		t.Fatalf("cmds = %d, want 1", len(cmds))
	}
	actionMsg, ok := cmds[0]().(workflowRunDialogActionMsg)
	if !ok || actionMsg.err == nil {
		t.Fatalf("cleanup on an active run must refuse: %#v", actionMsg)
	}
	if !strings.Contains(actionMsg.err.Error(), "cleanup requires a finished run") {
		t.Fatalf("cleanup refusal = %v", actionMsg.err)
	}
}

// snapshotDialogSnapshotTOML is the two-step definition frozen in a run's
// admitted snapshot. The workspace file changed AFTER the run started (it
// gained a third step); the dialog must describe the RUN, so its step list
// comes from the snapshot, never the changed file.
const snapshotDialogSnapshotTOML = `version = 1
name = "test-wf"
initial_step = "one"

[[steps]]
id = "one"
kind = "agent"
agent = "one"

[[steps]]
id = "two"
kind = "agent"
agent = "two"

[[transitions]]
from = "one"
to = "two"
match = { status = "succeeded" }

[[transitions]]
from = "two"
to = "success"
match = { status = "succeeded" }
`

// snapshotDialogCurrentTOML is the workspace definition after the run was
// admitted: it gained a third step that the run never executed.
const snapshotDialogCurrentTOML = `version = 1
name = "test-wf"
initial_step = "one"

[[steps]]
id = "one"
kind = "agent"
agent = "one"

[[steps]]
id = "two"
kind = "agent"
agent = "two"

[[steps]]
id = "three"
kind = "agent"
agent = "three"

[[transitions]]
from = "one"
to = "two"
match = { status = "succeeded" }

[[transitions]]
from = "two"
to = "three"
match = { status = "succeeded" }

[[transitions]]
from = "three"
to = "success"
match = { status = "succeeded" }
`

// newSnapshotDialogFixture builds a workspace whose run snapshot freezes
// snapshotDialogSnapshotTOML while the on-disk definition is
// snapshotDialogCurrentTOML, with succeeded attempts for the snapshot's two
// steps and the run settled at delivery_pending. It returns the root and
// config path that workflowRunDialogLoad resolves.
func newSnapshotDialogFixture(t *testing.T, runID string) (string, string) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	configBody := `[provider]
name = "openrouter"
[providers.openrouter]
base_url = "https://example.com"
api_key_env = "WORKFLOW_EVENTS_TEST_KEY"
models = [{ name = "test/model", context_window_tokens = 128000 }]
[subagents]
store_backend = "sqlite"
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	work, err := workspace.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	res, err := config.Load(config.LoadOptions{ConfigPath: configPath, WorkspaceRoot: work.Abs, AllowMissingConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	cli.ApplyPrivacyPolicy(res)
	cli.ApplyWorkflowStoreRoot(res, work.Abs)
	store, _, closeFn, err := cli.OpenWorkflowStore(work.Abs, res.Subagents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeFn)
	repo := workflowledger.NewStorageRepository(store)
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte(snapshotDialogSnapshotTOML), DefinitionDigest: "snap-dialog",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// CreateRun admits only pending runs; advance through running to
	// delivery_pending like the controller does.
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: runID, WorkflowName: "test-wf", Status: workflowledger.RunStatusPending, ActiveStepID: "one",
	}, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, runID, stored.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ id, to string }{{"one", "two"}, {"two", "success"}} {
		attemptID := "wfa-" + step.id + "-1"
		if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{
			AttemptID: attemptID, RunID: runID, StepID: step.id, AttemptNo: 1,
			CoordinatorRunID: "coord-" + step.id, TaskID: "task-" + step.id,
		}); err != nil {
			t.Fatal(err)
		}
		if err := repo.CompleteStepAttempt(ctx, runID, attemptID, 1, workflowledger.AttemptOutcome{
			Status: workflowledger.AttemptStatusSucceeded, ToStepID: step.to,
		}); err != nil {
			t.Fatal(err)
		}
	}
	writeWorkflowDefinition(t, root, "test-wf", snapshotDialogCurrentTOML)
	return root, configPath
}

// TestWorkflowRunDialogLoadPrefersSnapshotDefinition pins the dialog's
// definition source: the step list and states come from the run's admitted
// snapshot definition, never the changed workspace file. A run admitted
// under a two-step definition whose workspace file later gained a third step
// must show exactly the two snapshot steps, both done - the third step never
// appears as a phantom pending row, and the run's own steps never read
// pending just because the file on disk moved on.
func TestWorkflowRunDialogLoadPrefersSnapshotDefinition(t *testing.T) {
	root, configPath := newSnapshotDialogFixture(t, "wfr-SNAPDLG1")
	data, err := workflowRunDialogLoad(root, configPath, "wfr-SNAPDLG1")
	if err != nil {
		t.Fatal(err)
	}
	if data.compiled == nil {
		t.Fatal("dialog load resolved no compiled definition")
	}
	if len(data.compiled.Steps) != 2 {
		t.Fatalf("compiled steps = %d, want the 2 snapshot steps (the current file declares 3)", len(data.compiled.Steps))
	}
	view, err := buildWorkflowRunView(data.run, data.compiled, data.attempts, data.approvals, time.Now(), workflowRunDeliveryClaim{})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.steps) != 2 {
		t.Fatalf("view steps = %d, want 2 from the snapshot definition", len(view.steps))
	}
	for i, want := range []string{"one", "two"} {
		if view.steps[i].id != want || view.steps[i].state != workflowStepDone {
			t.Fatalf("step %d = %#v, want %s done", i, view.steps[i], want)
		}
	}
	d := &workflowRunDialog{runID: "wfr-SNAPDLG1", view: view}
	panel, _ := d.ViewAt(90, 24)
	text := cli.StripANSI(panel)
	if strings.Contains(text, "three") {
		t.Fatalf("dialog leaked a step from the changed workspace definition:\n%s", text)
	}
	if strings.Contains(text, "[pending]") {
		t.Fatalf("finished snapshot steps must not render pending:\n%s", text)
	}
}
