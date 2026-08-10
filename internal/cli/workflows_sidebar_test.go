package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

func workflowTestRows() []workflowRunRow {
	return []workflowRunRow{
		{run: workflowledger.RunSnapshot{
			RunID: "wfr-AAA1", WorkflowName: "alpha", Status: workflowledger.RunStatusRunning, ActiveStepID: "plan", StartedAt: time.Unix(100, 0),
		}, description: "Runs the alpha flow.", nextStep: "review"},
		{run: workflowledger.RunSnapshot{
			RunID: "wfr-BBB2", WorkflowName: "beta", Status: workflowledger.RunStatusPending, StartedAt: time.Unix(90, 0),
		}},
		{run: workflowledger.RunSnapshot{
			RunID: "wfr-CCC3", WorkflowName: "gamma", Status: workflowledger.RunStatusSucceeded, ActiveStepID: "success", StartedAt: time.Unix(200, 0),
		}},
	}
}

// TestWorkflowsSidebarEmptyListRendersNoRunsRow pins the empty-list row.
func TestWorkflowsSidebarEmptyListRendersNoRunsRow(t *testing.T) {
	s := newWorkflowsSidebar()
	view := stripANSI(s.view(28, 20, false))
	if !strings.Contains(view, " no workflow runs") {
		t.Fatalf("empty sidebar missing the no-runs row:\n%s", view)
	}
	if !strings.Contains(view, " Workflows · 0 runs") {
		t.Fatalf("empty sidebar header is wrong:\n%s", view)
	}
}

// TestWorkflowsSidebarActiveFirstOrdering pins that rows keep the caller's
// active-first order: the sidebar renders the list as given, with active
// statuses above terminal runs (workflowSidebarLoad performs the sort).
func TestWorkflowsSidebarActiveFirstOrdering(t *testing.T) {
	s := newWorkflowsSidebar()
	s.rows = workflowTestRows()
	view := stripANSI(s.view(28, 20, false))
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "beta") || !strings.Contains(view, "gamma") {
		t.Fatalf("sidebar is missing a workflow name:\n%s", view)
	}
	alphaAt := strings.Index(view, "alpha")
	gammaAt := strings.Index(view, "gamma")
	if alphaAt < 0 || gammaAt < 0 || gammaAt < alphaAt {
		t.Fatalf("active run must render above the terminal run:\n%s", view)
	}
}

// TestWorkflowsSidebarRowShowsDotNameAndStep pins the row shape: status dot,
// workflow name, and the active step with "-" when the step is empty.
func TestWorkflowsSidebarRowShowsDotNameAndStep(t *testing.T) {
	s := newWorkflowsSidebar()
	s.rows = workflowTestRows()
	view := stripANSI(s.view(28, 20, false))
	if !strings.Contains(view, "step plan") {
		t.Fatalf("row missing its active step:\n%s", view)
	}
	if !strings.Contains(view, "step -") {
		t.Fatalf("stepless row missing the dash fallback:\n%s", view)
	}
	if !strings.Contains(view, "◔") {
		t.Fatalf("running row missing its status dot:\n%s", view)
	}
}

// TestWorkflowsSidebarSelectedRowExpands pins the selected-row detail lines:
// description and next step render below the metadata line.
func TestWorkflowsSidebarSelectedRowExpands(t *testing.T) {
	s := newWorkflowsSidebar()
	s.rows = workflowTestRows()
	s.cursor = 0
	view := stripANSI(s.view(28, 24, true))
	if !strings.Contains(view, "Runs the alpha flow.") {
		t.Fatalf("selected row missing the description:\n%s", view)
	}
	if !strings.Contains(view, "next: review") {
		t.Fatalf("selected row missing the next step:\n%s", view)
	}
}

// TestWorkflowsSidebarSelectedRowWithoutDetails pins that a selected row with
// an unknown definition renders no detail lines.
func TestWorkflowsSidebarSelectedRowWithoutDetails(t *testing.T) {
	s := newWorkflowsSidebar()
	s.rows = workflowTestRows()
	s.cursor = 1 // beta has no description and no next step
	view := stripANSI(s.view(28, 24, true))
	if strings.Contains(view, "Runs the alpha flow.") || strings.Contains(view, "next:") {
		t.Fatalf("selected row without details rendered a detail line:\n%s", view)
	}
}

// TestWorkflowsSidebarTruncationRuneSafe pins that long names and long step
// ids truncate to the row width without splitting multi-byte runes (DC-6).
func TestWorkflowsSidebarTruncationRuneSafe(t *testing.T) {
	long := strings.Repeat("\U0001F642", 40) // 40 multi-byte runes
	s := newWorkflowsSidebar()
	s.rows = []workflowRunRow{{run: workflowledger.RunSnapshot{
		RunID: "wfr-LONG1", WorkflowName: long, Status: workflowledger.RunStatusRunning, ActiveStepID: long,
	}}}
	view := s.view(24, 20, false)
	if !utf8.ValidString(view) {
		t.Fatalf("sidebar output is not valid UTF-8")
	}
	for _, line := range strings.Split(stripANSI(view), "\n") {
		if runeWidth(line) > 24 {
			t.Fatalf("line width %d exceeds 24: %q", runeWidth(line), line)
		}
	}
}

// TestWorkflowsSidebarCursorMovementAndScrollClamp pins cursor movement and
// scroll clamping across many rows.
func TestWorkflowsSidebarCursorMovementAndScrollClamp(t *testing.T) {
	s := newWorkflowsSidebar()
	for i := 0; i < 20; i++ {
		s.rows = append(s.rows, workflowRunRow{run: workflowledger.RunSnapshot{
			RunID: "wfr-SC" + itoa(i), WorkflowName: "wf-" + itoa(i), Status: workflowledger.RunStatusRunning,
		}})
	}
	s.move(s.rows, 5)
	if s.cursor != 5 {
		t.Fatalf("cursor after +5 = %d, want 5", s.cursor)
	}
	s.move(s.rows, 100)
	if s.cursor != 19 {
		t.Fatalf("cursor after +100 = %d, want 19 (clamped)", s.cursor)
	}
	s.move(s.rows, -100)
	if s.cursor != 0 {
		t.Fatalf("cursor after -100 = %d, want 0 (clamped)", s.cursor)
	}
	// A short sidebar keeps the selected row visible: moving to the last row
	// must not push it off the bottom.
	s.cursor = 19
	s.scroll = 0
	s.clampScroll(s.rows, 6)
	tops := s.rowTops(s.rows)
	selBottom := tops[s.cursor] + workflowRunRowLines(s.rows[s.cursor], true)
	if s.scroll < 0 || s.scroll+6 < selBottom {
		t.Fatalf("scroll = %d, want the last row (bottom %d) fully visible in a 6-line region", s.scroll, selBottom)
	}
	sel, ok := s.selected(s.rows)
	if !ok || sel.run.RunID != "wfr-SC19" {
		t.Fatalf("selected = %#v ok=%v, want the last row", sel, ok)
	}
}

// TestWorkflowsSidebarHeaderAndFooter pins the header run count and the
// footer key hints.
func TestWorkflowsSidebarHeaderAndFooter(t *testing.T) {
	s := newWorkflowsSidebar()
	s.rows = workflowTestRows()
	view := stripANSI(s.view(28, 20, false))
	if !strings.Contains(view, " Workflows · 3 runs") {
		t.Fatalf("header missing the run count:\n%s", view)
	}
	if !strings.Contains(view, "Enter details") || !strings.Contains(view, "Esc close") {
		t.Fatalf("footer missing key hints:\n%s", view)
	}
}

// TestWorkflowsSidebarMoveEmptyList pins that movement on an empty list is a
// safe no-op.
func TestWorkflowsSidebarMoveEmptyList(t *testing.T) {
	s := newWorkflowsSidebar()
	s.move(nil, 1)
	if s.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 on an empty list", s.cursor)
	}
	if _, ok := s.selected(nil); ok {
		t.Fatal("selected on an empty list returned a row")
	}
}

// TestWorkflowSidebarLoadSortsActiveFirst pins the refresh seam ordering:
// active statuses sort above terminal runs, newest first inside each group.
func TestWorkflowSidebarLoadSortsActiveFirst(t *testing.T) {
	root, _, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-SORT001")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	snapshotJSON, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{
		SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d",
	})
	if err != nil {
		t.Fatal(err)
	}
	// CreateRun admits only pending runs; the terminal run must pass through
	// running first (pending -> running -> succeeded).
	if err := repo.CreateRun(ctx, workflowledger.RunSnapshot{
		RunID: "wfr-SORT002", WorkflowName: "test-wf", Status: workflowledger.RunStatusPending,
		ActiveStepID: "start", StartedAt: time.Unix(200, 0),
	}, snapshotJSON); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetRun(ctx, "wfr-SORT002")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-SORT002", stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-SORT002", stored.Version+1, workflowledger.RunStatusSucceeded, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := workflowSidebarLoad(root, filepath.Join(root, "config.toml"))
	if err != nil {
		t.Fatalf("workflowSidebarLoad: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// The active (pending) run must sort above the terminal run even though
	// the terminal run started later.
	if workflowledger.IsTerminalRunStatus(rows[0].run.Status) {
		t.Fatalf("rows[0] status = %s, want active first", rows[0].run.Status)
	}
	if rows[0].run.RunID != "wfr-SORT001" {
		t.Fatalf("rows[0] run = %s, want the seeded pending run", rows[0].run.RunID)
	}
	if !workflowledger.IsTerminalRunStatus(rows[1].run.Status) {
		t.Fatalf("rows[1] status = %s, want terminal below active", rows[1].run.Status)
	}
	if rows[0].nextStep != "review" {
		t.Fatalf("rows[0].nextStep = %q, want review (definition resolved)", rows[0].nextStep)
	}
	if rows[0].description != "Runs the test workflow." {
		t.Fatalf("rows[0].description = %q, want the definition description", rows[0].description)
	}
	if rows[1].nextStep != "" {
		t.Fatalf("terminal row nextStep = %q, want empty", rows[1].nextStep)
	}
}

// TestWorkflowSidebarLoadLedgerFailure pins that a failed ledger read returns
// the error so the caller keeps the previous rows.
func TestWorkflowSidebarLoadLedgerFailure(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-LOADFAIL1")
	defer closeFn()
	original := workflowRunsList
	t.Cleanup(func() { workflowRunsList = original })
	sentinel := errors.New("injected list failure")
	workflowRunsList = func(ctx context.Context, repo workflowledger.Repository, filter ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
		return nil, sentinel
	}
	if _, err := workflowSidebarLoad(root, ""); err == nil {
		t.Fatal("error = nil, want the injected ledger failure")
	}
}

// TestWorkflowsSidebarRefreshCmdIsAsync pins that refreshWorkflowsSidebar
// returns a tea.Cmd (so the ledger read never blocks the update goroutine)
// that delivers a workflowsSidebarRefreshMsg, and that a throttled call
// returns nil and marks the sidebar dirty while a closed sidebar issues
// nothing. Executing the returned command is exactly what bubbletea does off
// the update goroutine; the test goroutine stands in for the command executor.
func TestWorkflowsSidebarRefreshCmdIsAsync(t *testing.T) {
	root, _, _, closeFn, _, _ := openEventsFixtureWithRun(t, "wfr-ASYNC1")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	m := newReadyChatModel(40, 100)
	m.workspaceDir = root
	res, err := config.Load(config.LoadOptions{ConfigPath: filepath.Join(root, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	m.config = res
	m.workflowsSidebar = newWorkflowsSidebar()

	cmd := m.refreshWorkflowsSidebar()
	if cmd == nil {
		t.Fatal("open unthrottled sidebar returned no refresh command")
	}
	msg := cmd()
	refresh, ok := msg.(workflowsSidebarRefreshMsg)
	if !ok {
		t.Fatalf("refresh command returned %T, want workflowsSidebarRefreshMsg", msg)
	}
	if refresh.err != nil {
		t.Fatalf("refresh err = %v, want nil", refresh.err)
	}
	if len(refresh.rows) != 1 {
		t.Fatalf("refresh rows = %d, want the seeded run", len(refresh.rows))
	}

	// A second call inside the throttle window returns nil and marks dirty.
	if cmd := m.refreshWorkflowsSidebar(); cmd != nil {
		t.Fatal("throttled refreshWorkflowsSidebar returned a command")
	}
	if !m.workflowsSidebar.dirty {
		t.Fatal("throttled refresh did not mark the sidebar dirty")
	}

	// A closed sidebar never issues a command.
	m.workflowsSidebar = nil
	if cmd := m.refreshWorkflowsSidebar(); cmd != nil {
		t.Fatal("closed sidebar issued a refresh command")
	}
}

// TestWorkflowsSidebarRefreshMsgAppliesRows pins the updateMessageImpl
// handler: delivered rows replace the sidebar rows and clear the dirty flag;
// a failed read keeps the previous rows; a closed sidebar ignores the message
// without panicking.
func TestWorkflowsSidebarRefreshMsgAppliesRows(t *testing.T) {
	m := newReadyChatModel(40, 100)
	m.workflowsSidebar = newWorkflowsSidebar()
	m.workflowsSidebar.dirty = true
	rows := []workflowRunRow{{run: workflowledger.RunSnapshot{
		RunID: "wfr-APP1", WorkflowName: "alpha", Status: workflowledger.RunStatusRunning,
	}}}

	_, _ = m.Update(workflowsSidebarRefreshMsg{rows: rows})
	if len(m.workflowsSidebar.rows) != 1 || m.workflowsSidebar.rows[0].run.RunID != "wfr-APP1" {
		t.Fatalf("delivered rows not applied: %#v", m.workflowsSidebar.rows)
	}
	if m.workflowsSidebar.dirty {
		t.Fatal("dirty flag not cleared after a successful refresh")
	}

	// A failed read keeps the previous rows.
	before := m.workflowsSidebar.rows
	_, _ = m.Update(workflowsSidebarRefreshMsg{err: errors.New("read failed")})
	if len(m.workflowsSidebar.rows) != len(before) {
		t.Fatal("failed refresh replaced the rows")
	}

	// A closed sidebar ignores the message without panicking.
	m.workflowsSidebar = nil
	_, _ = m.Update(workflowsSidebarRefreshMsg{rows: rows})
}
