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
// workflow name, and the active step with "-" when the step is empty. A
// running row without a heartbeat reads stale, so its dot is the stale
// marker.
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
	if !strings.Contains(view, "!") {
		t.Fatalf("heartbeat-less running row missing the stale marker:\n%s", view)
	}
}

// TestWorkflowsSidebarTerminalStepShowsStatus pins that a run at or after
// the success/failure terminal never renders the reserved terminal step id
// ("success"/"failure") as a step: the derived active step for such a run is
// a marker of completion, not a declared step, so the metadata line shows
// the run's settled status instead.
func TestWorkflowsSidebarTerminalStepShowsStatus(t *testing.T) {
	s := newWorkflowsSidebar()
	s.rows = []workflowRunRow{
		{run: workflowledger.RunSnapshot{RunID: "wfr-TERM1", WorkflowName: "alpha", Status: workflowledger.RunStatusSucceeded, ActiveStepID: "success"}},
		{run: workflowledger.RunSnapshot{RunID: "wfr-TERM2", WorkflowName: "beta", Status: workflowledger.RunStatusDeliveryPending, ActiveStepID: "success"}},
		{run: workflowledger.RunSnapshot{RunID: "wfr-TERM3", WorkflowName: "gamma", Status: workflowledger.RunStatusFailed, ActiveStepID: "failure"}},
	}
	view := stripANSI(s.view(28, 20, false))
	if strings.Contains(view, "step success\n") {
		t.Fatalf("reserved terminal step rendered as a real step:\n%s", view)
	}
	for _, want := range []string{"step succeeded", "step delivery_pending", "step failed"} {
		if !strings.Contains(view, want) {
			t.Fatalf("terminal row missing %q:\n%s", want, view)
		}
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
	original := WorkflowRunsList
	t.Cleanup(func() { WorkflowRunsList = original })
	sentinel := errors.New("injected list failure")
	WorkflowRunsList = func(ctx context.Context, repo workflowledger.Repository, filter ...workflowledger.RunStatus) ([]workflowledger.RunSnapshot, error) {
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

// TestWorkflowHeartbeatFresh pins the freshness predicate shared by the
// sidebar dot and the dialog heartbeat line: a zero heartbeat is never
// fresh, one inside the window is fresh, and one at or past the window edge
// is stale.
func TestWorkflowHeartbeatFresh(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name        string
		heartbeatAt time.Time
		window      time.Duration
		want        bool
	}{
		{"zero heartbeat is never fresh", time.Time{}, workflowHeartbeatFreshWindow, false},
		{"fresh well inside the window", now.Add(-10 * time.Second), workflowHeartbeatFreshWindow, true},
		{"fresh exactly at the window edge", now.Add(-workflowHeartbeatFreshWindow), workflowHeartbeatFreshWindow, true},
		{"stale past the window", now.Add(-workflowHeartbeatFreshWindow - time.Second), workflowHeartbeatFreshWindow, false},
		{"future heartbeat (clock skew) is fresh", now.Add(5 * time.Second), workflowHeartbeatFreshWindow, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workflowHeartbeatFresh(tc.heartbeatAt, now, tc.window); got != tc.want {
				t.Fatalf("workflowHeartbeatFresh = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHeartbeatPulsePhaseAlternates pins that the pulse phase flips every
// heartbeatPulsePeriod, so a fresh heartbeat dot visibly animates across
// uiTick renders without sidebar state or extra ledger reads.
func TestHeartbeatPulsePhaseAlternates(t *testing.T) {
	start := time.Unix(0, 0)
	if !heartbeatPulsePhase(start) {
		t.Fatalf("phase at t=0 = false, want true")
	}
	if heartbeatPulsePhase(start.Add(heartbeatPulsePeriod)) {
		t.Fatalf("phase did not flip after one period")
	}
	if !heartbeatPulsePhase(start.Add(2 * heartbeatPulsePeriod)) {
		t.Fatalf("phase did not flip back after two periods")
	}
}

// TestWorkflowsSidebarRunDotHeartbeatStates pins the heartbeat dot states: a
// fresh heartbeat pulses (both pulse glyphs render across phases), a stale or
// missing heartbeat shows the stale marker, and non-running statuses keep the
// static status dot.
func TestWorkflowsSidebarRunDotHeartbeatStates(t *testing.T) {
	// The pulse dot must alternate glyphs across the two phases.
	phaseA := sidebarPulseDot(time.Unix(0, 0), false)
	phaseB := sidebarPulseDot(time.Unix(0, 0).Add(heartbeatPulsePeriod), false)
	if phaseA == phaseB {
		t.Fatalf("pulse dot must alternate across phases, both %q", phaseA)
	}
	for _, glyph := range []string{phaseA, phaseB} {
		if glyph != "◔" && glyph != heartbeatPulseGlyph {
			t.Fatalf("pulse dot glyph %q, want the thinking glyph or %q", glyph, heartbeatPulseGlyph)
		}
	}
	if got := sidebarStaleDot(false); got != "!" {
		t.Fatalf("stale dot = %q, want !", got)
	}

	s := newWorkflowsSidebar()
	s.rows = []workflowRunRow{
		{run: workflowledger.RunSnapshot{RunID: "wfr-HBD1", WorkflowName: "alpha", Status: workflowledger.RunStatusRunning, ActiveStepID: "plan"}, heartbeatAt: time.Now().Add(-5 * time.Second)},
		{run: workflowledger.RunSnapshot{RunID: "wfr-HBD2", WorkflowName: "beta", Status: workflowledger.RunStatusRunning, ActiveStepID: "plan"}, heartbeatAt: time.Now().Add(-2 * workflowHeartbeatFreshWindow)},
		{run: workflowledger.RunSnapshot{RunID: "wfr-HBD3", WorkflowName: "gamma", Status: workflowledger.RunStatusSucceeded, ActiveStepID: "success"}},
	}
	view := stripANSI(s.view(28, 20, false))
	if !strings.Contains(view, "!") {
		t.Fatalf("stale running row missing the stale marker:\n%s", view)
	}
	if !strings.Contains(view, "●") {
		t.Fatalf("terminal row lost the static idle dot:\n%s", view)
	}
	if !strings.Contains(view, "◔") && !strings.Contains(view, heartbeatPulseGlyph) {
		t.Fatalf("fresh running row missing the pulsing dot:\n%s", view)
	}
}

// TestWorkflowsSidebarRunDotDeliveryClaimStates pins the delivery_pending dot
// states: a fresh execution claim pulses (a delivery attempt is in flight), a
// stale claim shows the stale marker (a delivery crashed mid-publish), and no
// claim keeps the static streaming dot (waiting for a delivery).
func TestWorkflowsSidebarRunDotDeliveryClaimStates(t *testing.T) {
	s := newWorkflowsSidebar()
	run := workflowledger.RunSnapshot{RunID: "wfr-DLV1", WorkflowName: "alpha", Status: workflowledger.RunStatusDeliveryPending}
	now := time.Now()
	pulse := s.renderRunDot(workflowRunRow{run: run, claimAt: now.Add(-5 * time.Second), claimOK: true}, false)
	if pulse != "◔" && pulse != heartbeatPulseGlyph {
		t.Fatalf("fresh-claim delivery_pending dot = %q, want a pulse glyph", pulse)
	}
	stale := s.renderRunDot(workflowRunRow{run: run, claimAt: now.Add(-workflowledger.DefaultClaimLease - time.Minute), claimOK: true}, false)
	if stale != "!" {
		t.Fatalf("stale-claim delivery_pending dot = %q, want the stale marker", stale)
	}
	waiting := s.renderRunDot(workflowRunRow{run: run}, false)
	if waiting == "!" || waiting == "◔" || waiting == heartbeatPulseGlyph {
		t.Fatalf("no-claim delivery_pending dot = %q, want the static streaming dot", waiting)
	}
}

// TestWorkflowActiveAttemptHeartbeat pins the active-attempt heartbeat
// derivation: the newest running attempt on the run's active step wins; a run
// with no running attempt on the active step falls back to the newest running
// attempt overall; terminal and non-running attempts never count.
func TestWorkflowActiveAttemptHeartbeat(t *testing.T) {
	run := workflowledger.RunSnapshot{RunID: "wfr-ATT1", ActiveStepID: "plan"}
	hb := time.Unix(1000, 0)
	attempt := func(id, step string, status workflowledger.AttemptStatus, started, hbAt time.Time) workflowledger.StepAttempt {
		return workflowledger.StepAttempt{AttemptID: id, RunID: run.RunID, StepID: step, Status: status, StartedAt: started, LastHeartbeatAt: hbAt}
	}
	cases := []struct {
		name     string
		attempts []workflowledger.StepAttempt
		want     time.Time
	}{
		{"no attempts", nil, time.Time{}},
		{"active-step running attempt wins over another step", []workflowledger.StepAttempt{
			attempt("att-1", "other", workflowledger.AttemptStatusRunning, time.Unix(1, 0), hb),
			attempt("att-2", "plan", workflowledger.AttemptStatusRunning, time.Unix(2, 0), hb.Add(time.Second)),
		}, hb.Add(time.Second)},
		{"newest running attempt wins on the active step", []workflowledger.StepAttempt{
			attempt("att-1", "plan", workflowledger.AttemptStatusRunning, time.Unix(1, 0), hb),
			attempt("att-2", "plan", workflowledger.AttemptStatusRunning, time.Unix(2, 0), hb.Add(time.Second)),
		}, hb.Add(time.Second)},
		{"falls back to the newest running attempt off the active step", []workflowledger.StepAttempt{
			attempt("att-1", "plan", workflowledger.AttemptStatusSucceeded, time.Unix(3, 0), hb),
			attempt("att-2", "other", workflowledger.AttemptStatusRunning, time.Unix(2, 0), hb.Add(time.Second)),
		}, hb.Add(time.Second)},
		{"terminal attempts never count", []workflowledger.StepAttempt{
			attempt("att-1", "plan", workflowledger.AttemptStatusSucceeded, time.Unix(2, 0), hb),
		}, time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workflowActiveAttemptHeartbeat(run, tc.attempts); !got.Equal(tc.want) {
				t.Fatalf("workflowActiveAttemptHeartbeat = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWorkflowSidebarLoadLoadsRunningAttemptHeartbeat pins that
// workflowSidebarLoad reads the active attempt's LastHeartbeatAt for RUNNING
// runs only: a running run carries its heartbeat, while pending and terminal
// runs stay zero.
func TestWorkflowSidebarLoadLoadsRunningAttemptHeartbeat(t *testing.T) {
	root, _, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-HBLOAD1")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	stored, err := repo.GetRun(ctx, "wfr-HBLOAD1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-HBLOAD1", stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateStepAttempt(ctx, workflowledger.StepAttempt{AttemptID: "att-hb", RunID: "wfr-HBLOAD1", StepID: "start", AttemptNo: 1}); err != nil {
		t.Fatal(err)
	}
	hb := time.Now().Add(-5 * time.Second)
	if err := repo.SetStepAttemptHeartbeat(ctx, "wfr-HBLOAD1", "att-hb", hb); err != nil {
		t.Fatal(err)
	}
	rows, err := workflowSidebarLoad(root, filepath.Join(root, "config.toml"))
	if err != nil {
		t.Fatalf("workflowSidebarLoad: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !rows[0].heartbeatAt.Equal(hb) {
		t.Fatalf("rows[0].heartbeatAt = %v, want %v", rows[0].heartbeatAt, hb)
	}
}

// TestWorkflowSidebarLoadCarriesDeliveryClaim pins that workflowSidebarLoad
// reads the run's execution claim for DELIVERY_PENDING runs: a held claim
// yields claimOK=true with its acquired_at (delivery in flight), and a run
// without a claim reads claimOK=false (waiting for a delivery).
func TestWorkflowSidebarLoadCarriesDeliveryClaim(t *testing.T) {
	root, _, repo, closeFn, ctx, _ := openEventsFixtureWithRun(t, "wfr-DLVS1")
	defer closeFn()
	writeWorkflowDefinition(t, root, "test-wf", testWorkflowDefinition)
	stored, err := repo.GetRun(ctx, "wfr-DLVS1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-DLVS1", stored.Version, workflowledger.RunStatusRunning, nil); err != nil {
		t.Fatal(err)
	}
	running, err := repo.GetRun(ctx, "wfr-DLVS1")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompareAndSetRunStatus(ctx, "wfr-DLVS1", running.Version, workflowledger.RunStatusDeliveryPending, nil); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	if err := repo.ClaimRun(ctx, "wfr-DLVS1", "wfdel-abc"); err != nil {
		t.Fatal(err)
	}
	rows, err := workflowSidebarLoad(root, configPath)
	if err != nil {
		t.Fatalf("workflowSidebarLoad: %v", err)
	}
	var claimed bool
	for _, row := range rows {
		if row.run.RunID == "wfr-DLVS1" {
			claimed = row.claimOK
			if !row.claimOK {
				t.Fatal("delivery_pending row with a held claim must carry claimOK=true")
			}
			if row.claimAt.IsZero() {
				t.Fatal("delivery_pending row claimAt is zero")
			}
		}
	}
	if !claimed {
		t.Fatal("the delivery_pending run was not found in the sidebar rows")
	}
}
