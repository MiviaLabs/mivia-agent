package conversation

// Stall-display tests for the files panel's subagent rows, driven by the
// incident where a subagent sat in "running" for over ten minutes after its
// final report was visible: its provider connection trickled bytes, so
// heartbeats kept arriving with a frozen step count and every liveness clock
// the row saw refreshed on any heartbeat. The pins here: heartbeats move the
// stall clock only on a CHANGED step count, any other progress or tool event
// moves it, "stalled" is derived at render time and never stored, terminal
// beats stalled, and a reused id starts with a fresh clock.

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestProgressAdvances is the resolution table for "did this progress
// update move the row forward".
func TestProgressAdvances(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		prev subagentRow
		next subagentRow
		want bool
	}{
		{
			name: "frozen_step_heartbeat_is_not_motion",
			prev: subagentRow{Status: "running", Step: 2, LastProgress: aStaleTimestamp()},
			next: subagentRow{Status: "running", Step: 2},
			want: false,
		},
		{
			name: "changed_step_heartbeat_is_motion",
			prev: subagentRow{Status: "running", Step: 2, LastProgress: aStaleTimestamp()},
			next: subagentRow{Status: "running", Step: 3},
			want: true,
		},
		{
			name: "unparseable_step_zero_is_never_motion",
			prev: subagentRow{Status: "running", Step: 0, LastProgress: aStaleTimestamp()},
			next: subagentRow{Status: "running", Step: 0},
			want: false,
		},
		{
			name: "first_parseable_step_on_step_zero_row_is_motion",
			prev: subagentRow{Status: "running", Step: 0, LastProgress: aStaleTimestamp()},
			next: subagentRow{Status: "running", Step: 1},
			want: true,
		},
		{
			name: "terminal_update_is_always_motion",
			prev: subagentRow{Status: "running", Step: 2, LastProgress: aStaleTimestamp()},
			next: subagentRow{Status: "completed", Step: 2},
			want: true,
		},
		{
			// A single step can carry many tool calls (e.g. several file
			// reads before the model's next full turn); Step alone would
			// read that whole stretch as frozen and risk a false "stalled"
			// badge while ToolCalls is visibly climbing.
			name: "changed_toolcalls_with_frozen_step_is_motion",
			prev: subagentRow{Status: "running", Step: 2, ToolCalls: 5, LastProgress: aStaleTimestamp()},
			next: subagentRow{Status: "running", Step: 2, ToolCalls: 6},
			want: true,
		},
		{
			name: "frozen_toolcalls_and_step_is_not_motion",
			prev: subagentRow{Status: "running", Step: 2, ToolCalls: 5, LastProgress: aStaleTimestamp()},
			next: subagentRow{Status: "running", Step: 2, ToolCalls: 5},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := progressAdvances(tc.prev, tc.next); got != tc.want {
				t.Fatalf("progressAdvances(%+v, %+v) = %v, want %v", tc.prev, tc.next, got, tc.want)
			}
		})
	}
}

func aStaleTimestamp() time.Time { return time.Now().Add(-2 * time.Hour) }

// newStallScreen returns a screen whose panel has a small, deterministic
// stall setup: rows are backdated by hand, so no test sleeps.
func newStallScreen(t *testing.T) Screen {
	t.Helper()
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.panel.stallThreshold = time.Hour
	return s
}

// TestPanelStallClockRules pins which updates refresh a row's
// LastProgress.
func TestPanelStallClockRules(t *testing.T) {
	t.Run("frozen_step_heartbeats_do_not_refresh_the_clock", func(t *testing.T) {
		s := newStallScreen(t)
		s.panel.observeAgentStart("task-a", "")
		s.panel.observeAgent("task-a", &uievent.Progress{Status: "running", Step: 2})
		anchored := s.panel.agents[0].LastProgress
		if anchored.IsZero() {
			t.Fatal("advancing heartbeat did not anchor the stall clock")
		}
		// The incident cadence: heartbeats every 30s, step count frozen.
		for i := 0; i < 3; i++ {
			s.panel.observeAgent("task-a", &uievent.Progress{Status: "running", Step: 2})
		}
		if !s.panel.agents[0].LastProgress.Equal(anchored) {
			t.Fatal("frozen-step heartbeat refreshed the stall clock")
		}
	})
	t.Run("changed_step_heartbeat_refreshes_the_clock", func(t *testing.T) {
		s := newStallScreen(t)
		s.panel.observeAgentStart("task-a", "")
		s.panel.observeAgent("task-a", &uievent.Progress{Status: "running", Step: 2})
		s.panel.agents[0].LastProgress = aStaleTimestamp()
		s.panel.observeAgent("task-a", &uievent.Progress{Status: "running", Step: 3})
		if got := s.panel.agents[0].LastProgress; got.IsZero() || got.After(time.Now()) {
			t.Fatalf("changed-step heartbeat left LastProgress = %v, want a fresh stamp", got)
		}
	})
	t.Run("unparseable_heartbeat_is_not_progress", func(t *testing.T) {
		s := newStallScreen(t)
		s.panel.observeAgentStart("task-a", "")
		s.panel.observeAgent("task-a", &uievent.Progress{Status: "running", Step: 2})
		anchored := s.panel.agents[0].LastProgress
		// Loop EventStep remaps carry raw Detail: uiadapter parses no step
		// and leaves Step 0. The row must keep its known step and clock.
		s.panel.observeAgent("task-a", &uievent.Progress{Status: "running", Step: 0})
		if !s.panel.agents[0].LastProgress.Equal(anchored) {
			t.Fatal("unparseable heartbeat refreshed the stall clock")
		}
		if s.panel.agents[0].Step != 2 {
			t.Fatalf("row step = %d, want the last known 2", s.panel.agents[0].Step)
		}
	})
	t.Run("terminal_progress_refreshes_the_clock", func(t *testing.T) {
		s := newStallScreen(t)
		s.panel.observeAgentStart("task-a", "")
		s.panel.agents[0].LastProgress = aStaleTimestamp()
		s.panel.observeAgent("task-a", &uievent.Progress{Status: "completed"})
		if s.panel.agents[0].LastProgress.IsZero() || s.panel.agents[0].LastProgress.After(time.Now()) {
			t.Fatal("terminal progress did not refresh the stall clock")
		}
	})
}

// TestPanelStallDisplay pins the derived display state: running past the
// threshold reads "stalled", qualifying progress reads back "running", a
// terminal status beats "stalled", and a reused id starts fresh.
func TestPanelStallDisplay(t *testing.T) {
	t.Run("non_terminal_row_past_threshold_renders_stalled", func(t *testing.T) {
		s := newStallScreen(t)
		s.panel.observeAgentStart("task-a", "")
		if got := s.panel.displayStatus(s.panel.agents[0]); got != "running" {
			t.Fatalf("fresh row display = %q, want running", got)
		}
		s.panel.agents[0].LastProgress = aStaleTimestamp()
		if got := s.panel.displayStatus(s.panel.agents[0]); got != "stalled" {
			t.Fatalf("stale row display = %q, want stalled", got)
		}
	})
	t.Run("qualifying_progress_flips_back_to_running", func(t *testing.T) {
		s := newStallScreen(t)
		s.panel.observeAgentStart("task-a", "")
		s.panel.agents[0].LastProgress = aStaleTimestamp()
		s.panel.observeAgent("task-a", &uievent.Progress{Status: "running", Step: 1})
		if got := s.panel.displayStatus(s.panel.agents[0]); got != "running" {
			t.Fatalf("advanced row display = %q, want running", got)
		}
	})
	t.Run("terminal_status_beats_stalled", func(t *testing.T) {
		s := newStallScreen(t)
		s.panel.observeAgentStart("task-a", "")
		s.panel.agents[0].LastProgress = aStaleTimestamp()
		if got := s.panel.displayStatus(s.panel.agents[0]); got != "stalled" {
			t.Fatalf("setup: display = %q, want stalled", got)
		}
		s.panel.observeAgentEnd("task-a", true)
		if got := s.panel.displayStatus(s.panel.agents[0]); got != "completed" {
			t.Fatalf("terminal row display = %q, want completed (terminal wins)", got)
		}
	})
	t.Run("stored_status_is_never_stalled", func(t *testing.T) {
		s := newStallScreen(t)
		s.panel.observeAgentStart("task-a", "")
		s.panel.agents[0].LastProgress = aStaleTimestamp()
		if s.panel.agents[0].Status != "running" {
			t.Fatalf("stored status = %q; stalled must only ever be derived", s.panel.agents[0].Status)
		}
	})
	t.Run("observe_agent_start_resets_a_reused_row", func(t *testing.T) {
		s := newStallScreen(t)
		// A row from a PRIOR run under the same id: terminal, stale clock.
		s.panel.observeAgent("task-a", &uievent.Progress{Status: "completed", Step: 4})
		s.panel.agents[0].LastProgress = aStaleTimestamp()
		s.panel.observeAgentStart("task-a", "")
		row := s.panel.agents[0]
		if row.Status != "running" {
			t.Fatalf("reused row status = %q, want running", row.Status)
		}
		if row.LastProgress.IsZero() || row.LastProgress.After(time.Now()) {
			t.Fatalf("reused row LastProgress = %v, want a fresh anchor", row.LastProgress)
		}
		if got := s.panel.displayStatus(row); got != "running" {
			t.Fatalf("reused row display = %q, want running (no instant stall)", got)
		}
	})
	t.Run("zero_threshold_disables_the_derivation", func(t *testing.T) {
		s := newStallScreen(t)
		s.panel.observeAgentStart("task-a", "")
		s.panel.stallThreshold = 0
		s.panel.agents[0].LastProgress = aStaleTimestamp()
		if got := s.panel.displayStatus(s.panel.agents[0]); got != "running" {
			t.Fatalf("disabled derivation display = %q, want the stored running", got)
		}
	})
}

// TestPanelRowsRenderStalledIndicator wires the derivation to the real render
// site: the sidebar visual indicator shows warning color/mark for stalled past
// the threshold and flips back to running mark on the next qualifying heartbeat.
func TestPanelRowsRenderStalledIndicator(t *testing.T) {
	s := newStallScreen(t)
	s.panel.observeAgentStart("task-a", "")
	s.panel.observeAgent("task-a", &uievent.Progress{Status: "running", Step: 2, TotalSteps: 5})
	s.panel.agents[0].LastProgress = aStaleTimestamp()

	rows := strings.Join(s.panelRows(80, 24), "\n")
	if strings.Contains(rows, "[stalled]") || strings.Contains(rows, "[running]") {
		t.Fatalf("rendered rows should not contain text status badge:\n%s", rows)
	}

	stalledMark := s.subagentMark("stalled")
	if !strings.Contains(rows, stalledMark) {
		t.Fatalf("rendered rows show no stalled visual indicator:\n%s", rows)
	}

	s.panel.observeAgent("task-a", &uievent.Progress{Status: "running", Step: 3, TotalSteps: 5})
	rows = strings.Join(s.panelRows(80, 24), "\n")
	runningMark := s.subagentMark("running")
	if !strings.Contains(rows, runningMark) {
		t.Fatalf("rendered rows show no running visual indicator after progress:\n%s", rows)
	}
}

// TestPanelRowsHighlightOnlyTheCursorRow pins the render-side counterpart of
// selectedAgent's positional fix: a fleet of same-named, same-status agents
// (four "reviewer" rows all "running") renders byte-identical rowLabel()
// text for every one of them. panelRows used to decide the ">" highlight by
// comparing each row's label against the picker's selected label, so every
// duplicate lit up at once. It must mark exactly the row under the cursor,
// by position.
func TestPanelRowsHighlightOnlyTheCursorRow(t *testing.T) {
	s := newStallScreen(t)
	s.panel.open, s.panel.focused = true, true
	for _, id := range []string{"task-a", "task-b", "task-c", "task-d"} {
		s.panel.observeAgentStart(id, "reviewer")
	}
	s.panel.selectNavKind(navAgent, 2) // highlight task-c, the third agent row

	rows := s.panelRows(80, 24)
	marked := 0
	for _, r := range rows {
		if strings.Contains(stripAnsiForTest(r), ">") {
			marked++
		}
	}
	if marked != 1 {
		t.Fatalf("expected exactly 1 highlighted row, got %d:\n%s", marked, strings.Join(rows, "\n"))
	}

	agent, ok := s.panel.selectedAgent()
	if !ok || agent.ID != "task-c" {
		t.Fatalf("selectedAgent() = %+v, ok=%v; want task-c to match the highlighted row", agent, ok)
	}
}
