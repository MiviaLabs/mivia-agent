package conversation

// Sidebar subagent-row progress tests, split out of filespanel_test.go to
// keep that file under the file-size cap: StartedAt/elapsed tracking, the
// ToolCalls counter, the terminal-status race guard, and the two-line
// Elapsed/Tools/Step row rendering these enable.

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestObserveAgentStart_ResetsStaleTerminalRow guards a reused task id: a
// resumed session's LoadHistory seeds rows "completed", and models reuse
// short ids ("task-1", "audit") across dispatches. A NEW agent-start on an
// id whose row is stuck terminal must reset it to running - otherwise the
// sidebar would badge a genuinely live dispatch as already finished. (The
// thread dialog's composer is unconditionally hidden regardless of this
// row's status - see openThread - so a stale terminal row no longer risks
// freezing the dialog read-only; it only risks a wrong status badge.)
func TestObserveAgentStart_ResetsStaleTerminalRow(t *testing.T) {
	var p panel
	p.observeAgentHistory("task-1", "completed")
	p.observeAgentStart("task-1", "invoke_subagent")
	if len(p.agents) != 1 {
		t.Fatalf("expected 1 agent row, got %d", len(p.agents))
	}
	if got := p.agents[0].Status; got != "running" {
		t.Errorf("expected the reused-id row reset to 'running', got %q", got)
	}
}

// TestObserveAgentStart_DoesNotDisturbRunningRow pins the counterpart: a
// duplicate start on an already-running row stays running (idempotent).
func TestObserveAgentStart_DoesNotDisturbRunningRow(t *testing.T) {
	var p panel
	p.observeAgentStart("task-1", "invoke_subagent")
	p.observeAgentStart("task-1", "invoke_subagent")
	if len(p.agents) != 1 || p.agents[0].Status != "running" {
		t.Errorf("expected one running row, got %+v", p.agents)
	}
}

// TestObserveAgentStart_StampsStartedAt pins the elapsed-time anchor: a
// fresh row's StartedAt is set the moment it starts, so the sidebar can
// compute a live "Elapsed" duration at render time without waiting on the
// next heartbeat.
func TestObserveAgentStart_StampsStartedAt(t *testing.T) {
	var p panel
	before := time.Now()
	p.observeAgentStart("task-1", "invoke_subagent")
	if got := p.agents[0].StartedAt; got.Before(before) || got.After(time.Now()) {
		t.Fatalf("StartedAt = %v, want a timestamp taken during this call", got)
	}
}

// TestObserveAgentStart_ResetsStaleStartedAt pins the reused-id counterpart:
// a stale terminal row reset to running gets a FRESH StartedAt, not the
// prior run's - otherwise a reused id would report the elapsed time of the
// run that already ended.
func TestObserveAgentStart_ResetsStaleStartedAt(t *testing.T) {
	var p panel
	p.observeAgentHistory("task-1", "completed")
	stale := p.agents[0].StartedAt
	time.Sleep(time.Millisecond)
	p.observeAgentStart("task-1", "invoke_subagent")
	if got := p.agents[0].StartedAt; !got.After(stale) {
		t.Fatalf("StartedAt = %v, want a fresh timestamp after the stale one %v", got, stale)
	}
}

// TestObserveAgent_CarriesToolCalls pins ToolCalls flowing through
// observeAgent exactly like Step already does: latest value wins.
func TestObserveAgent_CarriesToolCalls(t *testing.T) {
	var p panel
	p.observeAgentStart("task-1", "invoke_subagent")
	p.observeAgent("task-1", &uievent.Progress{Status: "running", Step: 2, ToolCalls: 5})
	if got := p.agents[0].ToolCalls; got != 5 {
		t.Fatalf("ToolCalls = %d, want 5", got)
	}
	p.observeAgent("task-1", &uievent.Progress{Status: "running", Step: 3, ToolCalls: 9})
	if got := p.agents[0].ToolCalls; got != 9 {
		t.Fatalf("ToolCalls = %d, want 9 after a second update", got)
	}
}

// TestObserveAgentIgnoresProgressAfterTerminal pins the fix for a settled
// subagent row flipping back to "running" with climbing Step/Tools: the
// heartbeat ticker goroutine and the per-step live-update path
// (subagents.MultiStepHandler.stepOnEvent) both write Progress
// concurrently with the final Done event, so a heartbeat racing the
// terminal transition can be delivered after it. Every Progress a
// heartbeat carries hardcodes Status "running" (translateSubagentHeartbeat
// in internal/uiadapter/event_kind.go), so observeAgent must never let one
// downgrade a row that has already settled - only an explicit
// observeAgentStart (a genuinely new run under a reused id) may revive it.
func TestObserveAgentIgnoresProgressAfterTerminal(t *testing.T) {
	var p panel
	p.observeAgentStart("task-1", "")
	p.observeAgent("task-1", &uievent.Progress{Status: "running", Step: 29, ToolCalls: 218})
	p.observeAgentEnd("task-1", true)

	p.observeAgent("task-1", &uievent.Progress{Status: "running", Step: 30, ToolCalls: 222})

	if got := p.agents[0].Status; got != "completed" {
		t.Fatalf("status = %q, want completed (a late progress update must not revive a terminal row)", got)
	}
	if got := p.agents[0].Step; got != 29 {
		t.Errorf("Step = %d, want unchanged at 29 - a late progress update must be a full no-op once terminal", got)
	}
	if got := p.agents[0].ToolCalls; got != 218 {
		t.Errorf("ToolCalls = %d, want unchanged at 218 - a late progress update must be a full no-op once terminal", got)
	}
}

// TestTimedOutSubagentRowSettles pins the timed_out vocabulary: a subagent
// whose done event (or per-task result) reports timed_out is over. The row
// must count as terminal, so a late observeAgentStart cannot revive it to
// running and a timed-out run cannot leave a row spinning. (This status
// arrives through agent.Event.Status on the done event since the terminal
// status vocabulary landed.)
func TestTimedOutSubagentRowSettles(t *testing.T) {
	if !isTerminalStatus("timed_out") {
		t.Fatal("timed_out must be a terminal status")
	}
	if isNonTerminalStatus("timed_out") {
		t.Fatal("timed_out must not be a non-terminal status")
	}

	var p panel
	p.observeAgentStart("task-t", "audit")
	if p.activeAgentCount() != 1 {
		t.Fatalf("activeAgentCount = %d, want 1 while running", p.activeAgentCount())
	}
	p.setAgentStatus("task-t", "timed_out")
	if got := p.agents[0].Status; got != "timed_out" {
		t.Fatalf("status = %q, want timed_out", got)
	}
	if p.activeAgentCount() != 0 {
		t.Errorf("activeAgentCount = %d, want 0 once timed_out settles", p.activeAgentCount())
	}
	// A genuine new start for a reused id restarts the row, exactly as it
	// does for a completed row.
	p.observeAgentStart("task-t", "audit")
	if got := p.agents[0].Status; got != "running" {
		t.Errorf("status after a new start = %q, want running (timed_out settles like completed, no more)", got)
	}
}

// TestStatusBadgeRoleCoversTerminalVocabulary pins the badge color for every
// status the sidebar row can carry. A status with no case in the switch
// silently falls through to theme.RoleInfo - the same role a "running" row
// gets - so a terminal status missing a case is visually indistinguishable
// from a row that is still active. timed_out joined the terminal vocabulary
// (isTerminalStatus) without a matching badge-color case; this pins it
// alongside the other terminal statuses so the class cannot regress again.
func TestStatusBadgeRoleCoversTerminalVocabulary(t *testing.T) {
	cases := []struct {
		status string
		want   theme.Role
	}{
		{"completed", theme.RoleSuccess},
		{"done", theme.RoleSuccess},
		{"failed", theme.RoleDanger},
		{"error", theme.RoleDanger},
		{"interrupted", theme.RoleDanger},
		{"timed_out", theme.RoleDanger},
		{"cancelled", theme.RoleFGSubtle},
		{"canceled", theme.RoleFGSubtle},
		{"thinking", theme.RoleWarning},
		{statusStalled, theme.RoleWarning},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			if got := statusBadgeRole(tc.status); got != tc.want {
				t.Fatalf("statusBadgeRole(%q) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
	// A row that has settled into a terminal status must never read the
	// same as an actively running one.
	if got := statusBadgeRole("timed_out"); got == statusBadgeRole("running") {
		t.Fatalf("timed_out badge role (%v) must differ from running's (%v)", got, statusBadgeRole("running"))
	}
}

// TestFormatElapsed pins the sidebar's compact elapsed label - the shape
// requested for the metrics line ("10m 40s"), not clichat.FormatDuration's
// "10m40s" (internal/ui/** may not import internal/clichat, UI isolation).
func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{45 * time.Second, "45s"},
		{10*time.Minute + 40*time.Second, "10m 40s"},
		{time.Hour + 5*time.Minute, "1h 05m"},
		{-time.Second, "0s"}, // a clock skew or a not-yet-started row never reads negative
	}
	for _, tc := range cases {
		if got := formatElapsed(tc.d); got != tc.want {
			t.Errorf("formatElapsed(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// TestElapsedForFreezesOnTerminal pins the freeze contract: a running row's
// elapsed reading grows with the clock, but a terminal row's freezes at
// LastProgress (stamped the moment it settled) rather than continuing to
// grow every render after the subagent already finished.
func TestElapsedForFreezesOnTerminal(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(90 * time.Second)
	running := subagentRow{Status: "running", StartedAt: start}
	if got := elapsedFor(running, now); got != 90*time.Second {
		t.Fatalf("running elapsedFor = %v, want 90s", got)
	}
	finishedAt := start.Add(40 * time.Second)
	done := subagentRow{Status: "completed", StartedAt: start, LastProgress: finishedAt}
	if got := elapsedFor(done, now); got != 40*time.Second {
		t.Fatalf("terminal elapsedFor = %v, want 40s (frozen at LastProgress, not %v)", got, now.Sub(start))
	}
}

// TestPanelAgentRowRendersElapsedToolsStep pins the two-line row shape:
// the agent's name/status/badge on line 1, an indented elapsed/tools/step
// metrics line on line 2 - moved out of the chat transcript and into the
// sidebar, per the UX request this replaces.
func TestPanelAgentRowRendersElapsedToolsStep(t *testing.T) {
	s := New(loadTheme(t), theme.TierASCII, nil, nil, nil, 40, fixedNow)
	row := subagentRow{
		ID: "task-1", Status: "running", Step: 29, ToolCalls: 142,
		StartedAt: fixedNow().Add(-(10*time.Minute + 40*time.Second)),
	}
	lines := s.panelAgentRow(row, 40, false)
	if len(lines) != 2 {
		t.Fatalf("panelAgentRow returned %d lines, want 2: %v", len(lines), lines)
	}
	if strings.Contains(ansi.Strip(lines[0]), "[running]") {
		t.Errorf("name line %q contains text status badge [running]", ansi.Strip(lines[0]))
	}
	// Visual indicator check:
	if !strings.Contains(lines[0], s.subagentMark("running")) {
		t.Errorf("name line %q missing visual indicator for running", lines[0])
	}
	// The elapsed time survives at every sidebar width; how many of the
	// other facts fit beside it is agentMetrics's business, pinned by
	// TestAgentMetricsDropsWholeFactsInsteadOfClipping.
	if plain := ansi.Strip(lines[1]); !strings.Contains(plain, "10m 40s") {
		t.Errorf("metrics line %q does not carry the elapsed time", plain)
	}
	if strings.Contains(ansi.Strip(lines[0]), "29/") {
		t.Errorf("name line %q still carries the old inline step badge", ansi.Strip(lines[0]))
	}
}

// TestAgentMetricsDropsWholeFactsInsteadOfClipping is the discriminator
// for the metrics line fitting the sidebar. The line used to be one fixed
// string handed to the width clipper, which on a narrow sidebar produced
// "Elapsed: 0s, Tools:" - a label with its number sliced off, so the row
// showed a fact's name and not the fact. Every surviving part must now be
// whole, and elapsed - what a reader watching a long run is actually
// watching - must be the last to go.
func TestAgentMetricsDropsWholeFactsInsteadOfClipping(t *testing.T) {
	row := subagentRow{Status: "running", Step: 29, ToolCalls: 142}
	elapsed := 10*time.Minute + 40*time.Second
	for _, inner := range []int{6, 10, 14, 20, 24, 30, 40, 60} {
		got := ansi.Strip(agentMetrics(row, elapsed, inner))
		if inner >= len(agentMetricsIndent)+len("10m 40s") && ansi.StringWidth(got) > inner {
			t.Errorf("inner=%d: metrics line %q is %d columns wide", inner, got, ansi.StringWidth(got))
		}
		if !strings.Contains(got, "10m 40s") {
			t.Errorf("inner=%d: elapsed was dropped before the other facts: %q", inner, got)
		}
		// No fact may appear half-written: every part the line kept must
		// carry its number.
		for _, half := range []string{"tool", "step"} {
			if strings.HasSuffix(strings.TrimSpace(got), half) {
				t.Errorf("inner=%d: metrics line ends mid-fact: %q", inner, got)
			}
		}
	}
	wide := ansi.Strip(agentMetrics(row, elapsed, 60))
	for _, want := range []string{"10m 40s", "142 tools", "step 29"} {
		if !strings.Contains(wide, want) {
			t.Errorf("a wide sidebar dropped %q: %q", want, wide)
		}
	}
}

// TestSubagentMarkAnimatesWithStatuslineFrame tests that animated states
// advance their glyph with the statusline frame.
func TestSubagentMarkAnimatesWithStatuslineFrame(t *testing.T) {
	th := loadTheme(t)
	s := New(th, theme.TierTrueColor, nil, nil, nil, 80, fixedNow)

	// At frame 0 vs frame 1 vs frame 2
	m0 := s.subagentMark("running")
	// Update statusline directly with TickMsg when active:
	s.statusline.Start("running", fixedNow())
	m1 := s.subagentMark("running")
	s.statusline, _ = s.statusline.Update(statusline.TickMsg{})
	m2 := s.subagentMark("running")

	if m1 == m2 {
		t.Errorf("subagentMark glyph did not animate across statusline frames: %q == %q", m1, m2)
	}
	_ = m0
}

// for two-line agent rows: clipping to a tight maxRows must drop or keep a
// whole group (an agent's name+metrics pair), never leave a metrics line
// stranded without its name line above it.
func TestPanelWindowGroupsNeverSplitsAGroup(t *testing.T) {
	groups := [][]string{
		{"header"},
		{"file-1"},
		{"agent-1-name", "agent-1-metrics"},
		{"agent-2-name", "agent-2-metrics"},
		{"agent-3-name", "agent-3-metrics"},
	}
	got := panelWindowGroups(groups, 4, 3, false)
	for _, g := range got {
		found := false
		for _, orig := range groups {
			if len(g) == len(orig) && (len(g) == 0 || g[0] == orig[0]) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("window returned a group not present intact in the input: %v (full window: %v)", g, got)
		}
	}
}

// TestObserveAgent_NamespacedIDMatchesRawProgress verifies that progress updates
// arriving with a raw task ID properly update a namespaced row registered by dispatch_tasks.
func TestObserveAgent_NamespacedIDMatchesRawProgress(t *testing.T) {
	var p panel
	p.observeAgentGroupStart("call-xyz", []string{"call-xyz:worker-1"}, nil)
	if len(p.agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(p.agents))
	}
	if p.agents[0].ID != "call-xyz:worker-1" {
		t.Fatalf("expected ID call-xyz:worker-1, got %q", p.agents[0].ID)
	}

	// Progress arrives with raw ID worker-1
	p.observeAgent("worker-1", &uievent.Progress{
		Status:    "running",
		Step:      3,
		ToolCalls: 7,
		Log:       []string{"running search"},
	})

	if len(p.agents) != 1 {
		t.Fatalf("expected still 1 agent row, but got %d (duplicate created)", len(p.agents))
	}
	if p.agents[0].Step != 3 || p.agents[0].ToolCalls != 7 {
		t.Errorf("expected Step=3 ToolCalls=7, got Step=%d ToolCalls=%d", p.agents[0].Step, p.agents[0].ToolCalls)
	}
	if len(p.agents[0].Log) != 1 || p.agents[0].Log[0] != "running search" {
		t.Errorf("expected log preserved, got %v", p.agents[0].Log)
	}
}

// TestObserveAgent_PreservesNameAndCountsOnPartialUpdate verifies that Name and
// existing counts are not wiped out when a subsequent progress event arrives.
func TestObserveAgent_PreservesNameAndCountsOnPartialUpdate(t *testing.T) {
	var p panel
	p.observeAgentStart("call-1:task-a", "Reviewer")
	p.observeAgent("call-1:task-a", &uievent.Progress{
		Status:    "running",
		Step:      2,
		ToolCalls: 5,
	})

	if p.agents[0].Name != "Reviewer" {
		t.Errorf("Name = %q, want 'Reviewer'", p.agents[0].Name)
	}

	// Progress arrives without step or tool calls
	p.observeAgent("call-1:task-a", &uievent.Progress{
		Status: "running",
		Log:    []string{"heartbeat tick"},
	})

	if p.agents[0].Name != "Reviewer" {
		t.Errorf("Name wiped to %q on partial update", p.agents[0].Name)
	}
	if p.agents[0].Step != 2 || p.agents[0].ToolCalls != 5 {
		t.Errorf("Counts wiped: Step=%d, ToolCalls=%d", p.agents[0].Step, p.agents[0].ToolCalls)
	}
}

func TestObserveAgentHistory_UpdatesRunningAgent(t *testing.T) {
	var p panel
	p.observeAgentStart("task-1", "worker")
	p.observeAgentHistory("task-1", "completed")
	if p.agents[0].Status != "completed" {
		t.Errorf("status = %q, want completed", p.agents[0].Status)
	}
}
