package cli

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/charmbracelet/bubbles/viewport"
)

// headlessTUI builds a minimal *tuiModel for tool-panel unit tests (no Program).
func headlessTUI(nTools int, focused bool, selected int) *tuiModel {
	now := time.Now()
	rows := make([]toolRow, nTools)
	for i := range rows {
		rows[i] = toolRow{
			Name:  "tool",
			Done:  i < nTools-1, // leave last running-ish for variety
			Start: now.Add(-time.Duration(nTools-i) * time.Second),
			End:   now,
		}
	}
	if nTools > 0 {
		// Prefer clear done state for first n-1; last open matches applyToolEvents appends.
		rows[nTools-1].Done = false
		rows[nTools-1].End = time.Time{}
	}
	m := &tuiModel{
		toolRows:  rows,
		toolPanel: toolPanelState{Selected: selected, Focused: focused},
		viewport:  viewport.New(80, 20),
		width:     80,
		height:    40,
	}
	m.toolPanel.ordered = orderToolIndices(m.toolRows)
	m.toolPanel.Scroll = clampToolScroll(
		m.toolPanel.Scroll, m.toolPanel.Selected, m.toolPanel.ordered, toolMaxVisibleRows,
	)
	return m
}

func TestApplyToolEventsFocusedPreservesSelection(t *testing.T) {
	t.Parallel()
	// 10 tools; user focused on an earlier tool.
	m := headlessTUI(10, true, 3)
	prev := m.toolPanel.Selected
	if prev != 3 {
		t.Fatalf("precondition Selected=%d", prev)
	}
	m.applyToolEvents([]bridgeToolEvt{{
		Start:  true,
		Name:   "new-tool",
		Detail: "args",
		At:     time.Now(),
	}})
	if len(m.toolRows) != 11 {
		t.Fatalf("toolRows=%d want 11", len(m.toolRows))
	}
	if m.toolPanel.Selected != prev {
		t.Fatalf("Focused: Selected changed %d → %d (must not pin to newest)", prev, m.toolPanel.Selected)
	}
	// ordered/scroll refreshed
	if len(m.toolPanel.ordered) != 11 {
		t.Fatalf("ordered=%d want 11", len(m.toolPanel.ordered))
	}
}

func TestApplyToolEventsUnfocusedSelectsNewest(t *testing.T) {
	t.Parallel()
	m := headlessTUI(10, false, 2)
	m.applyToolEvents([]bridgeToolEvt{{
		Start:  true,
		Name:   "newest",
		Detail: "x",
		At:     time.Now(),
	}})
	want := len(m.toolRows) - 1
	if m.toolPanel.Selected != want {
		t.Fatalf("Focused=false: Selected=%d want newest %d", m.toolPanel.Selected, want)
	}
	if m.toolRows[want].Name != "newest" {
		t.Fatalf("newest row name=%q", m.toolRows[want].Name)
	}
}

func TestApplyToolEventsCompletionDoesNotStealFocusSelection(t *testing.T) {
	t.Parallel()
	m := headlessTUI(3, true, 0)
	// Complete the open tool (last row is open in headlessTUI).
	openName := m.toolRows[2].Name
	m.applyToolEvents([]bridgeToolEvt{{
		Start:  false,
		Name:   openName,
		Detail: "ok",
		At:     time.Now(),
	}})
	if m.toolPanel.Selected != 0 {
		t.Fatalf("completion changed Selected to %d", m.toolPanel.Selected)
	}
	if !m.toolRows[2].Done {
		t.Fatal("expected tool marked done")
	}
}

func TestApplyToolEventsMatchesDuplicateNamesByCallID(t *testing.T) {
	t.Parallel()
	m := headlessTUI(0, false, 0)
	m.applyToolEvents([]bridgeToolEvt{
		{Start: true, ToolCallID: "call-a", Name: "read_file", Detail: "a"},
		{Start: true, ToolCallID: "call-b", Name: "read_file", Detail: "b"},
		{Start: false, ToolCallID: "call-a", Name: "read_file", Detail: "result-a"},
	})
	if !m.toolRows[0].Done || m.toolRows[0].Result != "result-a" {
		t.Fatalf("first duplicate row not completed by ID: %+v", m.toolRows[0])
	}
	if m.toolRows[1].Done {
		t.Fatalf("second duplicate row was completed by wrong ID: %+v", m.toolRows[1])
	}
}

func TestApplyToolEventsRunningStatusUpdatesSameRow(t *testing.T) {
	t.Parallel()
	m := headlessTUI(0, false, 0)
	m.applyToolEvents([]bridgeToolEvt{
		{Start: true, ToolCallID: "c1", Name: "read_file", Detail: "queued"},
		{Start: true, ToolCallID: "c1", Name: "read_file", Detail: "running"},
	})
	if len(m.toolRows) != 1 {
		t.Fatalf("rows=%d want 1 (status update, not new row)", len(m.toolRows))
	}
	if m.toolRows[0].Detail != "running" {
		t.Fatalf("detail=%q want running", m.toolRows[0].Detail)
	}
	if m.toolRows[0].Done {
		t.Fatal("must still be open while running")
	}
}

func TestOnEventForMultiStepForwardsHeartbeat(t *testing.T) {
	t.Parallel()
	var got []agent.Event
	fn := OnEventForMultiStep(func(e agent.Event) { got = append(got, e) })
	fn(agent.Event{Kind: agent.EventSubagentHeartbeat, Detail: "elapsed=30s steps=2"})
	fn(agent.Event{Kind: agent.EventStep, Detail: "1/∞"})
	fn(agent.Event{Kind: agent.EventToolStart, ToolCallID: "t1", Name: "read_file", Detail: "running"})
	if len(got) != 3 {
		t.Fatalf("got %d events", len(got))
	}
	if got[0].Kind != agent.EventSubagentHeartbeat || got[0].Detail != "elapsed=30s steps=2" {
		t.Fatalf("heartbeat: %+v", got[0])
	}
	if got[1].Kind != agent.EventSubagentHeartbeat {
		t.Fatalf("step should map to heartbeat: %+v", got[1])
	}
	if got[2].Kind != agent.EventSubagentStart || got[2].ToolCallID != "t1" {
		t.Fatalf("tool start: %+v", got[2])
	}
}

func TestScrollWindowDoesNotMutateViewportYOffset(t *testing.T) {
	t.Parallel()
	m := headlessTUI(20, true, 0)
	m.viewport.SetContent(stringsJoinLines(40, "transcript line"))
	m.viewport.YOffset = 7
	if m.viewport.YOffset != 7 {
		t.Fatalf("precondition YOffset=%d", m.viewport.YOffset)
	}
	// Wheel path in Update calls scrollWindow only; assert isolation here.
	before := m.viewport.YOffset
	m.toolPanel.scrollWindow(+2, toolMaxVisibleRows)
	m.toolPanel.scrollWindow(-1, toolMaxVisibleRows)
	if m.viewport.YOffset != before {
		t.Fatalf("scrollWindow mutated viewport.YOffset %d → %d", before, m.viewport.YOffset)
	}
}

func stringsJoinLines(n int, prefix string) string {
	b := make([]byte, 0, n*(len(prefix)+8))
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, '\n')
		}
		b = append(b, prefix...)
	}
	return string(b)
}
