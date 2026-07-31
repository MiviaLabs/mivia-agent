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
	m.toolPanel.reindex(m.toolRows)
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
	// Only the tool finished in this batch is committed; pre-Done rows stay
	// until forceCommit at turn end. Selection on index 0 should remain.
	if m.toolPanel.Selected != 0 {
		t.Fatalf("completion changed Selected to %d", m.toolPanel.Selected)
	}
	if len(m.toolRows) != 2 {
		t.Fatalf("expected 2 remaining live rows (pre-done), got %d", len(m.toolRows))
	}
	found := false
	for _, b := range m.blocks {
		if b.Kind == ChatBlockTool {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected completed tool in chat history")
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
	// call-a committed to history; call-b remains open in live panel.
	if len(m.toolRows) != 1 || m.toolRows[0].ToolCallID != "call-b" || m.toolRows[0].Done {
		t.Fatalf("live rows after partial complete: %+v", m.toolRows)
	}
	found := false
	for _, b := range m.blocks {
		if b.Kind == ChatBlockTool && b.ToolCallID == "call-a" {
			found = true
			if !strings.Contains(b.Text, "result-a") {
				t.Fatalf("history tool text=%q", b.Text)
			}
		}
	}
	if !found {
		t.Fatal("expected call-a as ChatBlockTool in history")
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

func TestApplyToolEndEvent_UppercaseFailedLifecycleSetsFailed(t *testing.T) {
	m := headlessTUI(0, false, 0)
	m.toolRows = []toolRow{{ToolCallID: "c1", Name: "run_command", Start: time.Now()}}
	if got := m.applyToolEndEvent(bridgeToolEvt{ToolCallID: "c1", Name: "run_command", Detail: "FAILED", At: time.Now()}); got != 0 {
		t.Fatalf("matched row=%d, want 0", got)
	}
	if !m.toolRows[0].Failed || m.toolRows[0].Result != "FAILED" {
		t.Fatalf("row=%+v, want failed lifecycle with reason", m.toolRows[0])
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
	d := m.bridge.Drain()
	tools := d.Tools
	done := d.Done
	doneErr := d.DoneErr
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
	// Live panel should only hold open tools; finished rows commit to history.
	for _, r := range m.toolRows {
		if r.Done {
			t.Fatalf("done tool still in live panel: %+v", r)
		}
		if r.Name == "parallel" {
			t.Fatalf("parallel banner stuck open in live panel: %+v", r)
		}
	}
	if len(m.toolRows) != 0 {
		t.Fatalf("live toolRows should be empty after all ends, got %d", len(m.toolRows))
	}
	var parallelBlocks, realToolBlocks int
	for _, b := range m.blocks {
		if b.Kind != ChatBlockTool {
			continue
		}
		if b.ToolName == "parallel" {
			parallelBlocks++
		} else if b.ToolName != "" {
			realToolBlocks++
		}
	}
	if parallelBlocks != 2 {
		t.Fatalf("expected 2 parallel tool ChatBlocks in history, got %d", parallelBlocks)
	}
	if realToolBlocks != 3 {
		t.Fatalf("expected 3 real tool ChatBlocks in history, got %d", realToolBlocks)
	}
	if got := m.bridge.ActiveTools(); got != 0 {
		t.Fatalf("activeTools=%d want 0 after all ends (parallel must not leak)", got)
	}
}

func TestPruneBannerDoesNotStayActive(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	cb := agentEventBridgeCallback(m.bridge)
	cb(agent.Event{Kind: agent.EventPrune, Detail: "pruned ~100 tokens"})
	d := m.bridge.Drain()
	tools := d.Tools
	m.applyToolEvents(tools)
	if len(m.toolRows) != 0 {
		t.Fatalf("prune should commit out of live panel, rows=%+v", m.toolRows)
	}
	found := false
	for _, b := range m.blocks {
		if b.Kind == ChatBlockTool && b.ToolName == "prune" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected prune ChatBlock in history")
	}
	if got := m.bridge.ActiveTools(); got != 0 {
		t.Fatalf("activeTools=%d after prune banner", got)
	}
}

// TestChatTimelineProgressiveBlocks verifies web-chat order:
// interim speech → tools → final assistant; multi-bubble between batches.
func TestChatTimelineProgressiveBlocks(t *testing.T) {
	t.Parallel()
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "what next", SentAt: time.Now()})

	// Intermediate speech + tools (content-then-tools path).
	m.updateFromDrain(bridgeDrain{
		ResetStream: true,
		Interim:     "I'll inspect the project layout first.",
		Tools: []bridgeToolEvt{
			{Start: true, ToolCallID: "t1", Name: "list_dir", Detail: `{"path":"."}`, At: time.Now()},
		},
	})
	if !hasAssistantText(m.blocks, "inspect the project") {
		t.Fatalf("expected interim assistant bubble, blocks=%v", blockKinds(m.blocks))
	}
	if len(m.toolRows) != 1 || m.toolRows[0].Done {
		t.Fatalf("expected 1 open live tool, got %+v", m.toolRows)
	}

	// Tool ends → history tool block.
	m.applyToolEvents([]bridgeToolEvt{
		{Start: false, ToolCallID: "t1", Name: "list_dir", Detail: "cmd/ internal/", At: time.Now()},
	})
	if len(m.toolRows) != 0 {
		t.Fatalf("tool should leave live panel after end: %+v", m.toolRows)
	}
	if !hasToolBlock(m.blocks, "list_dir") {
		t.Fatal("expected list_dir ChatBlock in history")
	}

	// Second batch speech bubble between tool rounds.
	m.updateFromDrain(bridgeDrain{
		Interim: "Next I'll read the entrypoint.",
		Tools: []bridgeToolEvt{
			{Start: true, ToolCallID: "t2", Name: "read_file", Detail: `{"path":"main.go"}`, At: time.Now()},
			{Start: false, ToolCallID: "t2", Name: "read_file", Detail: "package main", At: time.Now()},
		},
	})
	if !hasAssistantText(m.blocks, "read the entrypoint") {
		t.Fatal("expected second interim assistant bubble")
	}

	// Final answer + finish.
	m.streamBuf.WriteString("Here is what is next.")
	_ = m.finishStream(nil)
	if m.waiting {
		t.Fatal("expected not waiting")
	}
	// Order: user → interim → tool → interim → tool → final assistant → done.
	kinds := blockKinds(m.blocks)
	if !kindOrderContains(kinds,
		ChatBlockUser, ChatBlockAssistant, ChatBlockTool,
		ChatBlockAssistant, ChatBlockTool, ChatBlockAssistant, ChatBlockDivider,
	) {
		t.Fatalf("unexpected multi-bubble timeline: %v", kinds)
	}
}

func hasAssistantText(blocks []ChatBlock, substr string) bool {
	for _, b := range blocks {
		if b.Kind == ChatBlockAssistant && strings.Contains(b.Text, substr) {
			return true
		}
	}
	return false
}

func hasBlockKind(blocks []ChatBlock, k ChatBlockKind) bool {
	for _, b := range blocks {
		if b.Kind == k {
			return true
		}
	}
	return false
}

func hasToolBlock(blocks []ChatBlock, name string) bool {
	for _, b := range blocks {
		if b.Kind == ChatBlockTool && b.ToolName == name {
			return true
		}
	}
	return false
}

func blockKinds(blocks []ChatBlock) []ChatBlockKind {
	out := make([]ChatBlockKind, len(blocks))
	for i, b := range blocks {
		out[i] = b.Kind
	}
	return out
}

func kindOrderContains(have []ChatBlockKind, want ...ChatBlockKind) bool {
	i := 0
	for _, h := range have {
		if i < len(want) && h == want[i] {
			i++
		}
	}
	return i == len(want)
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
