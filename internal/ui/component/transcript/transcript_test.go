package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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

// TestRenderGolden pins the cockpit's two renderings of the same
// recorded-conversation fixture internal/ui/stream proves its plain
// renderer against.
//
// The viewport golden is what the user sees: exactly the visible rows at
// a known size. The dump golden is the whole conversation expanded,
// which is what the write-to-scrollback key hands back to the terminal
// (docs/design/cockpit-research.md rule 6.3). Both matter, and neither
// substitutes for the other: the viewport can be right while the dump
// drops content, and the dump can be right while the viewport draws the
// wrong slice.
func TestRenderGolden(t *testing.T) {
	const width, height = 80, 20

	events, err := stream.DefaultFixture()
	if err != nil {
		t.Fatal(err)
	}
	m := New(loadTheme(t), theme.TierTrueColor)
	m.SetSize(width, height)
	for _, ev := range events {
		m, _ = m.HandleEvent(ev)
	}

	view := m.View()
	compareGolden(t, filepath.Join("testdata", "golden", "cockpit-80x20.txt"), view)
	compareGolden(t, filepath.Join("testdata", "golden", "transcript-dump.txt"), m.Dump())

	// Properties, independent of the bytes. A regenerated golden records
	// whatever the code does; these state what it MUST do, so a wrong
	// regeneration still fails.
	rows := strings.Split(view, "\n")
	if len(rows) != height {
		t.Errorf("viewport drew %d rows, want exactly %d", len(rows), height)
	}
	for _, line := range rows {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("row is %d columns, wider than the %d-column terminal: %q", w, width, line)
		}
	}
	// Following the tail means the last block is on screen.
	if !m.Following() {
		t.Error("the transcript stopped following the tail with no user scroll")
	}
}

func compareGolden(t *testing.T, path, got string) {
	t.Helper()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output does not match golden %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
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
	m.SetSize(80, 200)
	m = drain(t, m, events)
	got := ansi.Strip(m.Dump())
	for _, want := range []string{"hi", "full reply", "3 words", "hidden", "run_command", "output line", "1 of 2", "done", "boom", "step 1", "step 2", "context 80% full", "failed", "10 in"} {
		if !strings.Contains(got, want) {
			t.Errorf("the transcript is missing %q:\n%s", want, got)
		}
	}
	// A completed turn adds no block of its own: turn state belongs to
	// the status row.
	if strings.Contains(got, "completed") {
		t.Errorf("expected turn.end to add no block, got:\n%s", got)
	}
}

func TestToolOutputEmptyChunkCommitsNothing(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m, cmd := m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput, Body: uievent.ToolOutputBody{}})
	if cmd != nil {
		t.Errorf("expected no commit Cmd for an empty, progress-less tool.output, got one yielding %v", cmd())
	}
	if got := m.View(); got != "" {
		t.Errorf("got %q, want no live tail either", got)
	}
}

func TestReasoningLiveTailUsesSubtleStyle(t *testing.T) {
	m := New(loadTheme(t), theme.TierTrueColor)
	m.SetSize(80, 24)
	m, cmd := m.HandleEvent(uievent.Event{Kind: uievent.KindReasoning, Body: uievent.ReasoningDeltaBody{Text: "thinking..."}})
	if cmd == nil {
		t.Fatal("expected the first reasoning delta to schedule a flush Cmd")
	}
	got := m.View()
	if !strings.Contains(got, "thinking...") {
		t.Fatalf("got %q, want the live reasoning tail present before the final word-count chunk", got)
	}
	wantStyle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Render("thinking...")
	if !strings.Contains(got, wantStyle) {
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
	m.SetSize(80, 24)
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

func TestTextEndWithEmptyTextCommitsNothing(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "a"}})
	m, cmd := m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: ""}})
	if cmd != nil {
		t.Errorf("expected no commit Cmd for an empty TextEnd, got one yielding %v", cmd())
	}
	if got := m.View(); got != "" {
		t.Errorf("got %q, want the pending buffer cleared even with no text to commit", got)
	}
}

// TestStreamingTailBecomesABlock pins the handover: while a span streams
// it is an unaddressable tail, and when it ends it becomes a block that
// can take focus and collapse.
func TestStreamingTailBecomesABlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "partial"}})
	if len(m.Blocks()) != 0 {
		t.Fatal("a streaming span must not be a block yet")
	}
	if !strings.Contains(ansi.Strip(m.View()), "partial") {
		t.Fatal("expected the streaming tail on screen")
	}

	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "final"}})
	if got := len(m.Blocks()); got != 1 {
		t.Fatalf("got %d blocks, want the finished span as one block", got)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "final") {
		t.Errorf("got %q, want the finished text on screen", view)
	}
	if strings.Contains(view, "partial") {
		t.Errorf("got %q, want the tail replaced by the final text, not appended", view)
	}
}

func TestToolOutputProgressUpdatesProgressBarInPlace(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "subagent"},
	})

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: "c1",
			Progress: &uievent.Progress{
				Step: 1, TotalSteps: 3, Status: "running", ElapsedSeconds: 5,
				Log: []string{"step 1 log"},
			},
		},
	})

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: "c1",
			Progress: &uievent.Progress{
				Step: 2, TotalSteps: 3, Status: "running", ElapsedSeconds: 10,
				Log: []string{"step 1 log", "step 2 log"},
			},
		},
	})

	live := m.Blocks()
	if len(live) != 1 {
		t.Fatalf("got %d live blocks, want 1", len(live))
	}
	body := strings.Join(live[0].Body, "\n")
	if strings.Count(body, "33%") > 0 {
		t.Errorf("expected 33%% progress bar replaced, but found in body: %q", body)
	}
	if strings.Count(body, "66%") != 1 {
		t.Errorf("expected 66%% progress bar once, got body: %q", body)
	}
}
