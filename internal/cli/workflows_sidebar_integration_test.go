package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
	tea "github.com/charmbracelet/bubbletea"
)

// integrationWorkflowModel builds a chat model whose workflow ledger and
// definition directory live in one fixture workspace, with the UIAdapter
// attached to a live event bus.
func integrationWorkflowModel(t *testing.T) (*tuiModel, *workflowledger.StorageRepository, context.Context, func()) {
	t.Helper()
	root, _, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-INTEG1")
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	m := newReadyChatModel(40, 100)
	m.workspaceDir = root
	// refreshWorkflowsSidebar resolves the config through the session config;
	// a nil m.config would fall back to a missing workspace project file and
	// open no store, so load the fixture config the way the CLI does.
	res, err := config.Load(config.LoadOptions{ConfigPath: filepath.Join(root, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	m.config = res
	bus := events.New()
	m.eventBus = bus
	m.uiAdapter = NewUIAdapter(bus, m.bridge)
	return m, repo, ctx, closeFn
}

func workflowHeartbeatEvent(taskID string) events.Event {
	return events.Event{
		Kind: events.KindWorkflowStepHeartbeat, Timestamp: time.Now(), AgentTask: taskID,
		Metadata: map[string]string{"run_id": "wfr-INTEG1"},
	}
}

func addSecondRun(t *testing.T, repo *workflowledger.StorageRepository, ctx context.Context) {
	t.Helper()
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	// CreateRun admits only pending runs; advance to running after admission.
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: "wfr-INTEG2", WorkflowName: "test-wf", Status: workflowledger.RunStatusPending, ActiveStepID: "start",
	}, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, "wfr-INTEG2")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-INTEG2", stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
}

// deliverWorkflowsRefresh runs the command Update returned and delivers the
// workflows sidebar refresh message back through Update, mirroring how the
// bubbletea event loop executes a BatchMsg: every batched command runs on its
// own goroutine and every resulting message is sent back to Update. The
// adapter poll command that shares the batch runs for its own message, but
// that message is not re-delivered, so the self-perpetuating poll chain
// cannot loop the test. A nil command (throttled refresh) is a no-op.
func deliverWorkflowsRefresh(t *testing.T, m *tuiModel, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	var msgs []tea.Msg
	switch out := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range out {
			if c == nil {
				continue
			}
			if msg := c(); msg != nil {
				msgs = append(msgs, msg)
			}
		}
	default:
		if out != nil {
			msgs = append(msgs, out)
		}
	}
	for _, msg := range msgs {
		if _, ok := msg.(workflowsSidebarRefreshMsg); ok {
			_, _ = m.Update(msg)
		}
	}
}

// TestIntegrationWorkflowsSidebarOpensAndRefreshes covers the whole journey:
// /workflows opens the right sidebar, the first tick populates it, a bus
// heartbeat delivered through the UIAdapter refreshes the rows, and esc closes
// the sidebar. Every ledger read runs off the update goroutine and lands via
// workflowsSidebarRefreshMsg.
func TestIntegrationWorkflowsSidebarOpensAndRefreshes(t *testing.T) {
	m, repo, ctx, closeFn := integrationWorkflowModel(t)
	defer closeFn()

	if !m.handleSlash("/workflows") {
		t.Fatal("/workflows was not handled")
	}
	if m.workflowsSidebar == nil {
		t.Fatal("workflows sidebar did not open")
	}
	if m.focus != focusWorkflowsSidebar {
		t.Fatalf("focus = %v, want focusWorkflowsSidebar", m.focus)
	}
	// The first population is async: no rows right after open; the next
	// uiTickMsg heartbeat issues the off-goroutine ledger read.
	if len(m.workflowsSidebar.rows) != 0 {
		t.Fatalf("rows = %d, want 0 right after open (refresh is async)", len(m.workflowsSidebar.rows))
	}
	_, cmd := m.Update(uiTickMsg{})
	deliverWorkflowsRefresh(t, m, cmd)
	if len(m.workflowsSidebar.rows) != 1 {
		t.Fatalf("rows = %d, want 1 after the tick-driven refresh", len(m.workflowsSidebar.rows))
	}

	// A run started in another terminal appears after a heartbeat event that
	// passes the updateMessageImpl gate (AgentTask is set).
	addSecondRun(t, repo, ctx)
	m.workflowsSidebar.lastRefresh = time.Time{}
	bus := m.eventBus
	bus.Publish(workflowHeartbeatEvent("task-bus"))
	bus.Flush()
	msg := m.uiAdapter.PollCmd()()
	ev, ok := msg.(uiEventMsg)
	if !ok {
		t.Fatalf("expected uiEventMsg, got %T", msg)
	}
	_, cmd = m.Update(ev)
	deliverWorkflowsRefresh(t, m, cmd)
	if len(m.workflowsSidebar.rows) != 2 {
		t.Fatalf("rows = %d, want 2 after the heartbeat refresh", len(m.workflowsSidebar.rows))
	}

	// esc closes the sidebar.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.workflowsSidebar != nil {
		t.Fatal("esc did not close the workflows sidebar")
	}
	if m.focus != focusComposer {
		t.Fatalf("focus after close = %v, want composer", m.focus)
	}
}

// TestIntegrationWorkflowsSidebarThrottlesRapidEvents pins the ~2s throttle:
// a rapid second event defers and marks the sidebar dirty instead of issuing
// a second ledger read, and the deferred state clears once the window passes.
func TestIntegrationWorkflowsSidebarThrottlesRapidEvents(t *testing.T) {
	m, repo, ctx, closeFn := integrationWorkflowModel(t)
	defer closeFn()
	m.handleSlash("/workflows")
	addSecondRun(t, repo, ctx)

	// First tick populates the sidebar and starts the throttle window.
	m.workflowsSidebar.lastRefresh = time.Time{}
	_, cmd := m.Update(uiTickMsg{})
	deliverWorkflowsRefresh(t, m, cmd)
	if len(m.workflowsSidebar.rows) != 2 {
		t.Fatalf("rows = %d, want 2 after the first refresh", len(m.workflowsSidebar.rows))
	}

	// A rapid event inside the window defers and marks the sidebar dirty; no
	// refresh command is issued, so the rows stay put.
	_, cmd = m.Update(uiEventMsg{event: workflowHeartbeatEvent("task-1")})
	if !m.workflowsSidebar.dirty {
		t.Fatal("rapid event did not mark the sidebar dirty")
	}
	deliverWorkflowsRefresh(t, m, cmd)
	if len(m.workflowsSidebar.rows) != 2 {
		t.Fatalf("rows = %d, want 2 (no refresh inside the window)", len(m.workflowsSidebar.rows))
	}

	// Once the window expires, the next event refreshes exactly once.
	m.workflowsSidebar.lastRefresh = time.Time{}
	_, cmd = m.Update(uiEventMsg{event: workflowHeartbeatEvent("task-2")})
	deliverWorkflowsRefresh(t, m, cmd)
	if len(m.workflowsSidebar.rows) != 2 {
		t.Fatalf("rows = %d, want 2 after the throttled refresh", len(m.workflowsSidebar.rows))
	}
}

// TestIntegrationWorkflowsSidebarIdleRefresh pins the idle path: a heartbeat
// with no AgentTask does not pass the updateMessageImpl gate, but the
// uiTickMsg throttle refreshes the rows while the sidebar is open, so runs
// started in other terminals appear.
func TestIntegrationWorkflowsSidebarIdleRefresh(t *testing.T) {
	m, repo, ctx, closeFn := integrationWorkflowModel(t)
	defer closeFn()
	m.handleSlash("/workflows")
	addSecondRun(t, repo, ctx)

	m.workflowsSidebar.lastRefresh = time.Time{}
	_, cmd := m.Update(uiEventMsg{event: events.Event{Kind: events.KindWorkflowStepHeartbeat, Timestamp: time.Now()}})
	deliverWorkflowsRefresh(t, m, cmd)
	if len(m.workflowsSidebar.rows) != 0 {
		t.Fatal("idle event must not pass the updateMessageImpl gate")
	}

	m.workflowsSidebar.lastRefresh = time.Time{}
	_, cmd = m.Update(uiTickMsg{})
	deliverWorkflowsRefresh(t, m, cmd)
	if len(m.workflowsSidebar.rows) != 2 {
		t.Fatalf("rows = %d, want 2 after the uiTickMsg refresh", len(m.workflowsSidebar.rows))
	}
}

// TestIntegrationWorkflowsSidebarLedgerFailureKeepsStaleRows pins that a
// failing ledger read keeps the previous rows and never panics.
func TestIntegrationWorkflowsSidebarLedgerFailureKeepsStaleRows(t *testing.T) {
	m, _, _, closeFn := integrationWorkflowModel(t)
	defer closeFn()
	m.handleSlash("/workflows")
	original := workflowRunsList
	t.Cleanup(func() { workflowRunsList = original })
	sentinel := errors.New("injected list failure")
	workflowRunsList = func(ctx context.Context, repo workflowledger.Repository, filter ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
		return nil, sentinel
	}
	m.workflowsSidebar.rows = []workflowRunRow{{run: workflowledger.RunSnapshot{RunID: "wfr-STALE1", WorkflowName: "test-wf"}}}

	m.workflowsSidebar.lastRefresh = time.Time{}
	_, cmd := m.Update(uiEventMsg{event: workflowHeartbeatEvent("task-3")})
	deliverWorkflowsRefresh(t, m, cmd)
	if len(m.workflowsSidebar.rows) != 1 || m.workflowsSidebar.rows[0].run.RunID != "wfr-STALE1" {
		t.Fatalf("failed ledger read must keep the stale rows: %#v", m.workflowsSidebar.rows)
	}
}

// TestIntegrationWorkflowsAndSessionsSidebarsCoexist pins that both sidebars
// stay visible and scroll independently.
func TestIntegrationWorkflowsAndSessionsSidebarsCoexist(t *testing.T) {
	m, _, _, closeFn := integrationWorkflowModel(t)
	defer closeFn()
	m.sessionsSidebar = newSessionsSidebar()
	m.sessions = []chat.SessionInfo{{Name: "one"}, {Name: "two"}}
	m.workflowsSidebar = newWorkflowsSidebar()
	m.workflowsSidebar.rows = workflowTestRows()

	m.setFocus(focusSidebar)
	m.handleSidebarKey("down")
	if m.sessionsSidebar.cursor != 1 {
		t.Fatalf("sessions cursor = %d, want 1", m.sessionsSidebar.cursor)
	}
	if m.workflowsSidebar.cursor != 0 {
		t.Fatalf("workflows cursor moved with the sessions cursor: %d", m.workflowsSidebar.cursor)
	}

	m.setFocus(focusWorkflowsSidebar)
	m.handleWorkflowsSidebarKey("down")
	if m.workflowsSidebar.cursor != 1 {
		t.Fatalf("workflows cursor = %d, want 1", m.workflowsSidebar.cursor)
	}
	if m.sessionsSidebar.cursor != 1 {
		t.Fatalf("sessions cursor moved with the workflows cursor: %d", m.sessionsSidebar.cursor)
	}
}

// TestIntegrationWorkflowsSidebarToggleCloseAndRefuse pins the toggle shape:
// a second /workflows closes the sidebar, and a narrow terminal refuses to
// open it.
func TestIntegrationWorkflowsSidebarToggleCloseAndRefuse(t *testing.T) {
	m, _, _, closeFn := integrationWorkflowModel(t)
	defer closeFn()

	if !m.handleSlash("/workflows") {
		t.Fatal("first /workflows was not handled")
	}
	if m.workflowsSidebar == nil {
		t.Fatal("first /workflows did not open the sidebar")
	}
	if !m.handleSlash("/workflows") {
		t.Fatal("second /workflows was not handled")
	}
	if m.workflowsSidebar != nil {
		t.Fatal("second /workflows did not close the sidebar")
	}

	m.width = 60
	if !m.handleSlash("/workflows") {
		t.Fatal("third /workflows was not handled")
	}
	if m.workflowsSidebar != nil {
		t.Fatal("narrow terminal opened the workflows sidebar")
	}
}

// deliverWorkflowDialogRefresh runs the returned command and delivers the
// workflowRunDialogRefreshMsg back through Update, mirroring
// deliverWorkflowsRefresh. The open dialog's first read and every throttled
// refresh land through this helper.
func deliverWorkflowDialogRefresh(t *testing.T, m *tuiModel, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	var msgs []tea.Msg
	switch out := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range out {
			if c == nil {
				continue
			}
			if msg := c(); msg != nil {
				msgs = append(msgs, msg)
			}
		}
	default:
		if out != nil {
			msgs = append(msgs, out)
		}
	}
	for _, msg := range msgs {
		if _, ok := msg.(workflowRunDialogRefreshMsg); ok {
			_, _ = m.Update(msg)
		}
	}
}

// openIntegrationWorkflowDialog opens the /workflows sidebar, refreshes it,
// and opens the run-detail dialog for the selected (only) row, delivering the
// first async ledger read so the dialog has a real view.
func openIntegrationWorkflowDialog(t *testing.T, m *tuiModel) {
	t.Helper()
	m.handleSlash("/workflows")
	m.workflowsSidebar.lastRefresh = time.Time{}
	_, cmd := m.Update(uiTickMsg{})
	deliverWorkflowsRefresh(t, m, cmd)
	if len(m.workflowsSidebar.rows) != 1 {
		t.Fatalf("rows = %d, want the seeded run", len(m.workflowsSidebar.rows))
	}
	if !m.handleWorkflowsSidebarKey("enter") {
		t.Fatal("enter was not handled")
	}
	if m.workflowRunDlg == nil {
		t.Fatal("enter did not open the run dialog")
	}
	cmd = m.takePendingWorkflowDialogCmd()[0]
	deliverWorkflowDialogRefresh(t, m, cmd)
	if m.workflowRunDlg.view == nil {
		t.Fatal("dialog has no view after the first refresh")
	}
}

// TestIntegrationWorkflowRunDialogOpensByEnter covers the open-by-enter
// journey: enter on a sidebar row opens the dialog, the async first read
// builds a view with header facts and the compiled step list, and the run's
// status drives the available actions.
func TestIntegrationWorkflowRunDialogOpensByEnter(t *testing.T) {
	m, _, _, closeFn := integrationWorkflowModel(t)
	defer closeFn()
	openIntegrationWorkflowDialog(t, m)

	if m.workflowRunDlg.runID != "wfr-INTEG1" {
		t.Fatalf("dialog run = %s, want wfr-INTEG1", m.workflowRunDlg.runID)
	}
	if m.focus != focusWorkflowsSidebar {
		t.Fatalf("focus = %v, want workflows sidebar", m.focus)
	}
	panel, _ := m.workflowRunDlg.ViewAt(90, 24)
	text := stripANSI(panel)
	for _, want := range []string{"workflow: test-wf", "description: Runs the test workflow.", "run: wfr-INTEG1", "status: pending", "steps (2):", "▶ [active] start", "[pending] review"} {
		if !strings.Contains(text, want) {
			t.Fatalf("dialog missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(stripANSI(m.workflowRunDlg.footer()), "c cancel") {
		t.Fatalf("pending run footer missing cancel:\n%s", stripANSI(m.workflowRunDlg.footer()))
	}
}

// TestIntegrationWorkflowRunDialogOpensByDoubleClick covers the open-by-
// double-click journey: a second click on the same row within the window
// selects and opens the dialog (a single click only selects).
func TestIntegrationWorkflowRunDialogOpensByDoubleClick(t *testing.T) {
	m, _, _, closeFn := integrationWorkflowModel(t)
	defer closeFn()
	m.workflowsSidebar = newWorkflowsSidebar()
	m.workflowsSidebar.rows = []workflowRunRow{{run: workflowledger.RunSnapshot{
		RunID: "wfr-INTEG1", WorkflowName: "test-wf", Status: workflowledger.RunStatusPending, ActiveStepID: "start",
	}}}
	m.setFocus(focusScrollback)
	pane := newChatPaneLayout(m.width, false, true)
	x := pane.rightSidebarX() + 1

	m.Update(tea.MouseMsg{Type: tea.MouseLeft, X: x, Y: workflowsRowsY})
	if m.workflowRunDlg != nil {
		t.Fatal("single click must not open the dialog")
	}
	_, cmd := m.Update(tea.MouseMsg{Type: tea.MouseLeft, X: x, Y: workflowsRowsY})
	if m.workflowRunDlg == nil {
		t.Fatal("double-click did not open the run dialog")
	}
	if m.workflowRunDlg.runID != "wfr-INTEG1" {
		t.Fatalf("dialog run = %s, want wfr-INTEG1", m.workflowRunDlg.runID)
	}
	deliverWorkflowDialogRefresh(t, m, cmd)
	if m.workflowRunDlg.view == nil {
		t.Fatal("dialog has no view after the double-click first read")
	}
}

// TestIntegrationWorkflowRunDialogRefreshesWhileOpen pins the refresh-while-
// open journey: a heartbeat event passes the updateMessageImpl gate and issues
// a throttled dialog refresh, and a rapid second event defers instead of
// issuing a second read.
func TestIntegrationWorkflowRunDialogRefreshesWhileOpen(t *testing.T) {
	m, repo, ctx, closeFn := integrationWorkflowModel(t)
	defer closeFn()
	openIntegrationWorkflowDialog(t, m)
	if m.workflowRunDlg.view.run.Status != workflowledger.RunStatusPending {
		t.Fatalf("initial status = %s, want pending", m.workflowRunDlg.view.run.Status)
	}

	// Advance the run: pending -> running with an attempt on the active step.
	stored, err := repo.GetRun(ctx, "wfr-INTEG1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-INTEG1", stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}

	// Make the dialog throttle window fresh so the rapid event below is
	// deterministically deferred regardless of how slowly the setup ran.
	m.workflowRunDlg.lastRefresh = time.Now()

	// A rapid heartbeat inside the dialog throttle window defers: the dialog
	// is marked dirty and no refresh command is issued.
	_, _ = m.Update(uiEventMsg{event: workflowHeartbeatEvent("task-dlg-1")})
	if !m.workflowRunDlg.dirty {
		t.Fatal("rapid event did not mark the dialog dirty")
	}

	// Once the window expires, the next tick refreshes exactly once and the
	// view reflects the new status.
	m.workflowRunDlg.lastRefresh = time.Time{}
	_, cmd := m.Update(uiTickMsg{})
	deliverWorkflowDialogRefresh(t, m, cmd)
	if m.workflowRunDlg.view.run.Status != workflowledger.RunStatusRunning {
		t.Fatalf("status after refresh = %s, want running", m.workflowRunDlg.view.run.Status)
	}
}

// TestIntegrationWorkflowRunDialogEnterNoOpOnEmptyList pins the negative path:
// enter on an empty /workflows list is a no-op and never opens a dialog.
func TestIntegrationWorkflowRunDialogEnterNoOpOnEmptyList(t *testing.T) {
	m, _, _, closeFn := integrationWorkflowModel(t)
	defer closeFn()
	m.workflowsSidebar = newWorkflowsSidebar()
	m.setFocus(focusWorkflowsSidebar)
	if m.handleWorkflowsSidebarKey("enter") {
		t.Fatal("enter on an empty list must be a no-op")
	}
	if m.workflowRunDlg != nil {
		t.Fatal("empty list opened a run dialog")
	}
}

// TestIntegrationWorkflowRunDialogEscCloseRestoresFocus pins that esc (and q)
// close the dialog and restore the workflows sidebar focus.
func TestIntegrationWorkflowRunDialogEscCloseRestoresFocus(t *testing.T) {
	m, _, _, closeFn := integrationWorkflowModel(t)
	defer closeFn()
	openIntegrationWorkflowDialog(t, m)

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.workflowRunDlg != nil {
		t.Fatal("esc did not close the dialog")
	}
	if m.focus != focusWorkflowsSidebar {
		t.Fatalf("focus after esc = %v, want workflows sidebar", m.focus)
	}

	// Re-open from the still-open sidebar and close with q.
	if !m.handleWorkflowsSidebarKey("enter") {
		t.Fatal("re-open enter was not handled")
	}
	cmd := m.takePendingWorkflowDialogCmd()[0]
	deliverWorkflowDialogRefresh(t, m, cmd)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if m.workflowRunDlg != nil {
		t.Fatal("q did not close the dialog")
	}
	if m.focus != focusWorkflowsSidebar {
		t.Fatalf("focus after q = %v, want workflows sidebar", m.focus)
	}
}
