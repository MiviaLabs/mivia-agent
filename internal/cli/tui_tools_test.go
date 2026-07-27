package cli

import (
	"strings"
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
		{Start: true, ToolCallID: "c1", Name: "delegate", Detail: `{"task":"analyze auth","multi_step":true}`},
		{Start: true, ToolCallID: "c1", Name: "delegate", Detail: "running"},
	})
	if len(m.toolRows) != 1 {
		t.Fatalf("rows=%d want 1 (status update, not new row)", len(m.toolRows))
	}
	// Status must not clobber operator-facing args Detail.
	if m.toolRows[0].Status != "running" {
		t.Fatalf("status=%q want running", m.toolRows[0].Status)
	}
	if !strings.Contains(m.toolRows[0].Detail, "analyze auth") {
		t.Fatalf("detail args lost: %q", m.toolRows[0].Detail)
	}
	if m.toolRows[0].Done {
		t.Fatal("must still be open while running")
	}
	// Collapsed summary must still show the task intent.
	sum := newToolRenderItem(m.toolRows[0].Name, m.toolRows[0].Detail, m.toolRows[0].Result, false, false).summary(80)
	if !strings.Contains(sum, "analyze auth") {
		t.Fatalf("operator summary=%q", sum)
	}
}

// TestParallelBannerDoesNotStayActive is the regression for sticky yellow
// "parallel queued N tools" rows: EventToolParallel must not leave open rows
// or inflate activeTools after the batch has real tool starts/ends.
func TestParallelBannerDoesNotStayActive(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	emitTwoParallelBatches(agentEventBridgeCallback(m.bridge))
	_, tools, done, doneErr, _, _, _, _ := m.bridge.Drain()
	if done || doneErr != nil {
		t.Fatalf("unexpected done=%v err=%v", done, doneErr)
	}
	m.applyToolEvents(tools)
	assertNoStickyParallel(t, m)
}

func emitTwoParallelBatches(cb func(agent.Event)) {
	cb(agent.Event{Kind: agent.EventToolParallel, Detail: "2 tools: list_dir, glob"})
	cb(agent.Event{
		Kind: agent.EventToolStart, ToolCallID: "c1", Name: "list_dir",
		Detail: "queued", Input: `{"path":"."}`,
	})
	cb(agent.Event{
		Kind: agent.EventToolStart, ToolCallID: "c2", Name: "glob",
		Detail: "queued", Input: `{"pattern":"*"}`,
	})
	cb(agent.Event{
		Kind: agent.EventToolStart, ToolCallID: "c1", Name: "list_dir", Detail: "running",
	})
	cb(agent.Event{
		Kind: agent.EventToolEnd, ToolCallID: "c1", Name: "list_dir",
		Detail: "completed", Output: "a.go\nb.go",
	})
	cb(agent.Event{
		Kind: agent.EventToolEnd, ToolCallID: "c2", Name: "glob",
		Detail: "completed", Output: "ok",
	})
	cb(agent.Event{Kind: agent.EventToolParallel, Detail: "2 tools: read_file, read_file"})
	cb(agent.Event{
		Kind: agent.EventToolStart, ToolCallID: "c3", Name: "read_file",
		Detail: "queued", Input: `{}`,
	})
	cb(agent.Event{
		Kind: agent.EventToolEnd, ToolCallID: "c3", Name: "read_file",
		Detail: "completed", Output: "x",
	})
}

func assertNoStickyParallel(t *testing.T, m *tuiModel) {
	t.Helper()
	var parallelOpen, parallelDone, realOpen, realDone int
	for _, r := range m.toolRows {
		switch r.Name {
		case "parallel":
			if r.Done {
				parallelDone++
			} else {
				parallelOpen++
			}
		default:
			if r.Done {
				realDone++
			} else {
				realOpen++
			}
		}
	}
	if parallelOpen != 0 {
		t.Fatalf("parallel banners still open (yellow forever): open=%d rows=%+v", parallelOpen, m.toolRows)
	}
	if parallelDone != 2 {
		t.Fatalf("expected 2 completed parallel banners, got done=%d", parallelDone)
	}
	if realOpen != 0 {
		t.Fatalf("real tools still open: open=%d", realOpen)
	}
	if realDone != 3 {
		t.Fatalf("expected 3 completed real tools, got %d", realDone)
	}
	if got := m.bridge.ActiveTools(); got != 0 {
		t.Fatalf("activeTools=%d want 0 after all ends (parallel must not leak)", got)
	}
	open, doneN, total := countTools(m.toolRows)
	if open != 0 || doneN != total {
		t.Fatalf("countTools open=%d done=%d total=%d", open, doneN, total)
	}
}

func TestPruneBannerDoesNotStayActive(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	cb := agentEventBridgeCallback(m.bridge)
	cb(agent.Event{Kind: agent.EventPrune, Detail: "pruned ~100 tokens"})
	_, tools, _, _, _, _, _, _ := m.bridge.Drain()
	m.applyToolEvents(tools)
	if len(m.toolRows) != 1 || m.toolRows[0].Name != "prune" || !m.toolRows[0].Done {
		t.Fatalf("prune banner: %+v", m.toolRows)
	}
	if got := m.bridge.ActiveTools(); got != 0 {
		t.Fatalf("activeTools=%d after prune banner", got)
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
