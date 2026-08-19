package transcript

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/stream"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

// TestRenderGolden pins the styled transcript output for the same
// recorded-conversation fixture internal/ui/stream proves its plain
// renderer against, so the two can be compared block-for-block.
func TestRenderGolden(t *testing.T) {
	events, err := stream.DefaultFixture()
	if err != nil {
		t.Fatal(err)
	}
	m := New(loadTheme(t), theme.TierTrueColor)
	for _, ev := range events {
		if ev.Kind == uievent.KindTextDelta || ev.Kind == uievent.KindReasoning {
			continue // batched; only the terminal TextEnd/final-chunk event commits a block
		}
		m, _ = m.HandleEvent(ev)
	}
	got := m.View()

	goldenPath := filepath.Join("testdata", "golden", "conversation.txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output does not match golden %s\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, want)
	}
}

func TestHandleEventEveryKind(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	events := []uievent.Event{
		{Kind: uievent.KindTurnStart, Body: uievent.TurnStartBody{Input: "hi"}},
		{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "chunk"}},
		{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "full reply"}},
		{Kind: uievent.KindReasoning, Body: uievent.ReasoningDeltaBody{Text: "thinking"}},
		{Kind: uievent.KindReasoning, Body: uievent.ReasoningDeltaBody{WordCount: 3}},
		{Kind: uievent.KindToolPending, Body: uievent.ToolPendingBody{Name: "run_command"}},
		{Kind: uievent.KindToolStart, Body: uievent.ToolStartBody{Name: "run_command"}},
		{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{Chunk: "output line"}},
		{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{Progress: &uievent.Progress{Step: 1, TotalSteps: 2, Status: "running"}}},
		{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{Name: "run_command", OK: true, Result: "done"}},
		{Kind: uievent.KindToolEnd, Body: uievent.ToolEndBody{Name: "edit", OK: false, Err: "boom"}},
		{Kind: uievent.KindPlan, Body: uievent.PlanBody{Items: []uievent.PlanItem{{Text: "step 1", Done: true}, {Text: "step 2"}}, Done: 1, Total: 2}},
		{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "context 80% full"}},
		{Kind: uievent.KindError, Body: uievent.ErrorBody{Text: "failed", Fatal: true}},
		{Kind: uievent.KindUsage, Body: uievent.UsageBody{InputTokens: 10, OutputTokens: 5}},
		{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
	for _, ev := range events {
		m, _ = m.HandleEvent(ev)
	}
	got := m.View()
	for _, want := range []string{"hi", "full reply", "3 words hidden", "run_command", "output line", "1/2", "done", "boom", "step 1", "step 2", "context 80% full", "failed", "10 in"} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript view missing %q:\n%s", want, got)
		}
	}
	// TurnEnd commits no block of its own.
	if strings.Contains(got, "completed") {
		t.Errorf("expected turn.end to commit no block, got:\n%s", got)
	}
}

func TestToolOutputEmptyChunkCommitsNoBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{}})
	if got := m.View(); got != "" {
		t.Errorf("got %q, want no block committed for an empty, progress-less tool.output", got)
	}
	if len(m.blocks) != 0 {
		t.Errorf("got %d blocks, want 0", len(m.blocks))
	}
}

func TestReasoningLiveTailUsesSubtleStyle(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindReasoning, Body: uievent.ReasoningDeltaBody{Text: "thinking..."}})
	got := m.View()
	if !strings.Contains(got, "thinking...") {
		t.Fatalf("got %q, want the live reasoning tail present before the final word-count chunk", got)
	}
	wantStyle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render("thinking...")
	if got != wantStyle {
		t.Errorf("got %q, want the reasoning tail styled with RoleFGSubtle: %q", got, wantStyle)
	}
}

func TestFlushCmdYieldsFlushMsg(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	_, cmd := m.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "a"}})
	if cmd == nil {
		t.Fatal("expected a Cmd")
	}
	if _, ok := cmd().(FlushMsg); !ok {
		t.Errorf("got %T, want the scheduled Cmd to yield FlushMsg", cmd())
	}
}

func TestTextDeltaBatchesAndSchedulesOneFlush(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m, cmd1 := m.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "a"}})
	if cmd1 == nil {
		t.Fatal("expected the first delta to schedule a flush Cmd")
	}
	m, cmd2 := m.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "b"}})
	if cmd2 != nil {
		t.Error("expected the second delta to not schedule another flush Cmd while one is pending")
	}
	if got := m.View(); !strings.Contains(got, "ab") {
		t.Errorf("expected the live pending buffer to show accumulated text, got %q", got)
	}
}

func TestUpdateFlushReschedulesWhileStreaming(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "a"}})
	next, cmd := m.Update(FlushMsg{})
	if cmd == nil {
		t.Error("expected Update to reschedule a flush while pending text remains")
	}
	m = next

	// Ignoring an unrelated Msg must be a no-op.
	next, cmd = m.Update(tea.WindowSizeMsg{})
	if cmd != nil {
		t.Error("expected Update to ignore non-FlushMsg messages")
	}
	_ = next
}

func TestUpdateFlushStopsAfterTextEnd(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "a"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "a"}})
	_, cmd := m.Update(FlushMsg{})
	if cmd != nil {
		t.Error("expected Update to stop rescheduling once the span has ended")
	}
}

func TestCommitBoundsToMaxBlocks(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.maxBlocks = 2
	for i := 0; i < 5; i++ {
		m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "n" + strconv.Itoa(i)}})
	}
	if len(m.blocks) != 2 {
		t.Fatalf("got %d blocks, want 2 (bounded)", len(m.blocks))
	}
	if !strings.Contains(m.blocks[0], "n3") || !strings.Contains(m.blocks[1], "n4") {
		t.Errorf("expected the newest 2 blocks to survive, got %v", m.blocks)
	}
}
