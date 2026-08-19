package transcript

import (
	"os"
	"path/filepath"
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

// commitText executes a Cmd and returns the text it carries, failing the
// test if it's not (or doesn't yield) a CommitMsg.
func commitText(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a commit Cmd, got nil")
	}
	msg, ok := cmd().(CommitMsg)
	if !ok {
		t.Fatalf("got %T, want CommitMsg", cmd())
	}
	return msg.Text
}

// handleAndCollectCommits drives every event through m, in order,
// collecting the text of every CommitMsg produced (delta/flush Cmds are
// ignored - deltas never commit on their own).
func handleAndCollectCommits(t *testing.T, m Model, events []uievent.Event) []string {
	t.Helper()
	var commits []string
	for _, ev := range events {
		var cmd tea.Cmd
		m, cmd = m.HandleEvent(ev)
		if cmd == nil {
			continue
		}
		if msg, ok := cmd().(CommitMsg); ok {
			commits = append(commits, msg.Text)
		}
	}
	return commits
}

// TestRenderGolden pins the styled commit stream for the same
// recorded-conversation fixture internal/ui/stream proves its plain
// renderer against: the ordered sequence of CommitMsg text a caller
// would tea.Println, joined by newlines.
func TestRenderGolden(t *testing.T) {
	events, err := stream.DefaultFixture()
	if err != nil {
		t.Fatal(err)
	}
	m := New(loadTheme(t), theme.TierTrueColor)
	got := strings.Join(handleAndCollectCommits(t, m, events), "\n")

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
	got := strings.Join(handleAndCollectCommits(t, m, events), "\n")
	for _, want := range []string{"hi", "full reply", "3 words hidden", "run_command", "output line", "1/2", "done", "boom", "step 1", "step 2", "context 80% full", "failed", "10 in"} {
		if !strings.Contains(got, want) {
			t.Errorf("commits missing %q:\n%s", want, got)
		}
	}
	// TurnEnd commits no block of its own.
	if strings.Contains(got, "completed") {
		t.Errorf("expected turn.end to commit no block, got:\n%s", got)
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
	m, cmd := m.HandleEvent(uievent.Event{Kind: uievent.KindReasoning, Body: uievent.ReasoningDeltaBody{Text: "thinking..."}})
	if cmd == nil {
		t.Fatal("expected the first reasoning delta to schedule a flush Cmd")
	}
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

func TestViewClearsOnceCommitted(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "a"}})
	if m.View() == "" {
		t.Fatal("expected a live tail while streaming")
	}
	m, cmd := m.HandleEvent(uievent.Event{Kind: uievent.KindTextEnd, Body: uievent.TextEndBody{Text: "a"}})
	if commitText(t, cmd) == "" {
		t.Error("expected TextEnd to commit the final text")
	}
	if got := m.View(); got != "" {
		t.Errorf("got %q, want an empty View() once the span is committed (it now lives in scrollback, not the model)", got)
	}
}
