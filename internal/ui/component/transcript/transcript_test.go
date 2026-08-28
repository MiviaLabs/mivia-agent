package transcript

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
	"github.com/MiviaLabs/mivia-agent/internal/ui/stream"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
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
	// EOL-normalized: a windows-latest checkout can materialize the LF
	// golden as CRLF (core.autocrlf=true); the model itself writes LF.
	gotNorm := strings.ReplaceAll(got, "\r\n", "\n")
	wantNorm := strings.ReplaceAll(string(want), "\r\n", "\n")
	if gotNorm != wantNorm {
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
		{Kind: uievent.KindHook, Body: uievent.HookBody{Event: "PostToolUse", Program: "fmt.sh", Tool: "write_file"}},
		{Kind: uievent.KindError, Body: uievent.ErrorBody{Text: "failed", Fatal: true}},
		{Kind: uievent.KindUsage, Body: uievent.UsageBody{InputTokens: 10, OutputTokens: 5}},
		{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
	m.SetSize(80, 200)
	m = drain(t, m, events)
	got := ansi.Strip(m.Dump())
	for _, want := range []string{"hi", "full reply", "3 words", "hidden", "run_command", "output line", "1 of 2", "done", "boom", "step 1", "step 2", "context 80% full", "fmt.sh", "failed", "10 in"} {
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
	wantStyle := render.Role(m.Theme, m.Tier, theme.RoleFGSubtle).Italic(true).Render("thinking...")
	if !strings.Contains(got, wantStyle) {
		t.Errorf("got %q, want the reasoning tail styled with RoleFGSubtle italic: %q", got, wantStyle)
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

// TestToolOutputProgressDoesNotChurnTheLiveBlock pins the current
// contract: subagent progress heartbeats no longer rewrite the tool
// call's block on every tick (that churn used to sit in the middle of the
// scrollback for the life of a long-running subagent - it now belongs to
// the sidebar panel only). The block pushed by tool.start stays exactly
// as pushed - no Meta, no Detail, no body - through any number of
// heartbeats, until tool.end renders its terminal state.
func TestToolOutputProgressDoesNotChurnTheLiveBlock(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 24)

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "subagent"},
	})
	pushed := m.Blocks()[0]

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: "c1",
			Progress: &uievent.Progress{
				Step: 1, TotalSteps: 3, ToolCalls: 4, Status: "running",
				Log: []string{"step 1 log"},
			},
		},
	})
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: "c1",
			Progress: &uievent.Progress{
				Step: 2, TotalSteps: 3, ToolCalls: 9, Status: "running",
				Log: []string{"step 1 log", "step 2 log"},
			},
		},
	})

	live := m.Blocks()
	if len(live) != 1 {
		t.Fatalf("got %d live blocks, want 1 (no new block pushed for progress)", len(live))
	}
	if !reflect.DeepEqual(live[0], pushed) {
		t.Errorf("block changed after progress events:\npushed: %+v\nafter:  %+v", pushed, live[0])
	}
}

func TestSetThemeReRendersBlocks(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetSize(80, 24)

	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: "```go\nfunc hello() {}\n```"},
	})

	origBody := m.Blocks()[0].Body
	origConcat := strings.Join(origBody, "\n")

	// Create a different theme. The Glamour-backed code block uses
	// RoleBGSubtle (outer background) and RoleString (foreground) for
	// its primitive colour, so flipping a role that flows into the
	// code block is what proves SetTheme re-renders it. The OLD
	// render.Text applied RoleBGInset to fenced lines, but that role
	// belongs to inline code (RoleKeyword + RoleBGInset) under the
	// new mapping; a test that flipped RoleBGInset only would still
	// pass for the wrong reason now (the inline code is not in body
	// row 0 for a fenced block).
	lightTheme := th
	lightTheme.Colors = make(map[theme.Role]string)
	for k, v := range th.Colors {
		lightTheme.Colors[k] = v
	}
	lightTheme.Colors[theme.RoleKeyword] = "#abcdef"
	lightTheme.Colors[theme.RoleFunction] = "#123456"

	m.SetTheme(lightTheme, theme.TierTrueColor)

	newConcat := strings.Join(m.Blocks()[0].Body, "\n")
	if newConcat == origConcat {
		t.Error("SetTheme failed to re-render code block with new theme colors")
	}
}

// TestSetThemeReRendersADiffMergedIntoALiveBlock is the reported bug at
// its own level. A tool call normally arrives as start-then-end, so the
// end merges into the live block instead of pushing a new one. That
// merge kept the rendered diff lines but dropped the raw diff, leaving
// SetTheme nothing to rebuild - the diff stayed in the previous theme's
// add/del colours while the rest of the screen changed.
func TestSetThemeReRendersADiffMergedIntoALiveBlock(t *testing.T) {
	dark, light := loadTheme(t), otherTheme(t)
	m := New(dark, theme.TierTrueColor)
	m.SetSize(80, 24)

	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "edit_file"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit_file", OK: true, Diff: sampleDiff()}})

	before := strings.Join(m.Blocks()[0].Body, "\n")
	m.SetTheme(light, theme.TierTrueColor)
	after := strings.Join(m.Blocks()[0].Body, "\n")

	want := render.SplitDiff(light, theme.TierTrueColor, 80-uikitconfig.BodyIndent, *sampleDiff())
	if !strings.Contains(after, want) {
		t.Errorf("the diff was not re-rendered in the new theme:\ngot  %q\nwant %q", after, want)
	}
	if after == before {
		t.Error("SetTheme left the merged diff on the previous theme's colours")
	}
}

// TestSetThemeKeepsToolOutputAboveTheDiff: a call that printed output
// before its diff keeps that output. Re-rendering must replace the diff
// at the end of the body, not the whole body.
func TestSetThemeKeepsToolOutputAboveTheDiff(t *testing.T) {
	dark, light := loadTheme(t), otherTheme(t)
	m := New(dark, theme.TierTrueColor)
	m.SetSize(80, 24)

	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "edit_file"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "c1", Chunk: "scanning up.go\n"}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "edit_file", OK: true, Diff: sampleDiff()}})

	rowsBefore := len(m.Blocks()[0].Body)
	m.SetTheme(light, theme.TierTrueColor)
	body := m.Blocks()[0].Body

	if len(body) != rowsBefore {
		t.Errorf("body is %d rows after the switch, was %d", len(body), rowsBefore)
	}
	if !strings.Contains(strings.Join(body, "\n"), "scanning up.go") {
		t.Errorf("the tool output above the diff was dropped: %q", body)
	}
}

// TestSetThemeKeepsAPlansCollapseState: rebuilding a plan's body must
// not reset what the reader did with the block.
func TestSetThemeKeepsAPlansCollapseState(t *testing.T) {
	dark, light := loadTheme(t), otherTheme(t)
	m := New(dark, theme.TierTrueColor)
	m.SetSize(80, 24)

	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindPlan, Body: uievent.PlanBody{
		Total: 2, Done: 1,
		Items: []uievent.PlanItem{{Text: "read", Done: true}, {Text: "write"}},
	}})
	id := m.Blocks()[0].ID
	m.blocks[0].Collapsed = true

	m.SetTheme(light, theme.TierTrueColor)

	if got := m.Blocks()[0].ID; got != id {
		t.Errorf("the plan block lost its identity: %q, want %q", got, id)
	}
	if !m.Blocks()[0].Collapsed {
		t.Error("the plan block was expanded by a theme change")
	}
}

// otherTheme is a second embedded theme, for switching away from the one
// loadTheme returns.
func otherTheme(t *testing.T) theme.Theme { return namedTheme(t, "mivia-light") }

func namedTheme(t *testing.T, name string) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == name {
			return th
		}
	}
	t.Fatalf("theme %q not embedded", name)
	return theme.Theme{}
}

func sampleDiff() *uievent.Diff {
	return &uievent.Diff{Path: "up.go", Added: 1, Removed: 1, Hunks: []uievent.DiffHunk{{
		Header: "@@ -1,2 +1,2 @@",
		Lines: []uievent.DiffLine{
			{Kind: uievent.DiffLineDel, Text: "return u.raw.Put(ctx)"},
			{Kind: uievent.DiffLineAdd, Text: "return retry.Do(ctx, put)"},
		},
	}}}
}

// TestProgressEventIsANoOpOnBothPaths pins the current contract on both
// paths a Progress event could take: a bare push (no prior tool.start) and
// a merge into an already-running tool call. Neither pushes or mutates a
// block - subagent progress is sidebar-only now (see
// TestToolOutputProgressDoesNotChurnTheLiveBlock and
// TestProgressEventIsANoOp for the individual-path pins this covers
// together, plus the both-paths-agree property the old progress-bar
// styling tests existed to prove for the removed bar).
func TestProgressEventIsANoOpOnBothPaths(t *testing.T) {
	p := &uievent.Progress{Step: 2, TotalSteps: 3, Status: "running", Log: []string{"read defaults.go"}}
	progress := uievent.Event{Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "c1", Progress: p}}

	pushed := New(loadTheme(t), theme.TierTrueColor)
	pushed.SetSize(80, 24)
	pushed, _ = pushed.HandleEvent(progress)
	if len(pushed.Blocks()) != 0 {
		t.Errorf("bare progress push: got %d blocks, want 0", len(pushed.Blocks()))
	}

	merged := New(loadTheme(t), theme.TierTrueColor)
	merged.SetSize(80, 24)
	merged, _ = merged.HandleEvent(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "subagent"}})
	before := merged.Blocks()[0]
	merged, _ = merged.HandleEvent(progress)
	if got, want := merged.Blocks(), []Block{before}; !reflect.DeepEqual(got, want) {
		t.Errorf("progress merge changed the live block:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestTranscriptSliceImmutability(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetSize(80, 24)

	m1, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindTurnStart,
		Body: uievent.TurnStartBody{Input: "hello world"},
	})
	oldModel1 := m1

	// Push another block
	m2, _ := m1.HandleEvent(uievent.Event{
		Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: "response"},
	})
	_ = m2

	if len(oldModel1.blocks) != 1 {
		t.Errorf("HandleEvent mutated previous transcript blocks slice len: got %d, want 1", len(oldModel1.blocks))
	}

	// SetSize with width change on m1
	m1Copy := oldModel1
	m1Copy.SetSize(40, 24)
	if &oldModel1.blocks[0].Body[0] == &m1Copy.blocks[0].Body[0] {
		t.Error("SetSize mutated userLines body on shared blocks slice")
	}

	// updateLive tool output
	mStart, _ := m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "c1", Name: "bash"},
	})
	oldStart := mStart
	mOut, _ := mStart.HandleEvent(uievent.Event{
		Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{ToolCallID: "c1", Chunk: "out1\n"},
	})
	_ = mOut
	if len(oldStart.blocks[0].Body) != 0 {
		t.Errorf("updateLive mutated previous model block body: got len %d", len(oldStart.blocks[0].Body))
	}
}

func TestAutoFoldReasoningPreservesBody(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetSize(80, 24)

	// Stream reasoning delta chunks
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindReasoning,
		Body: uievent.ReasoningDeltaBody{Text: "thinking deeply...\nsecond thought line"},
	})
	// Finalize with word count
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindReasoning,
		Body: uievent.ReasoningDeltaBody{WordCount: 5},
	})

	if len(m.Blocks()) != 1 {
		t.Fatalf("expected 1 block, got %d", len(m.Blocks()))
	}
	blk := m.Blocks()[0]
	if blk.Kind != uievent.KindReasoning {
		t.Errorf("expected KindReasoning, got %v", blk.Kind)
	}
	if !blk.Collapsible {
		t.Error("reasoning block must be collapsible")
	}
	if !blk.Collapsed {
		t.Error("reasoning block must be collapsed by default on completion")
	}
	if len(blk.Body) != 2 {
		t.Fatalf("expected preserved body with 2 lines, got %d lines: %v", len(blk.Body), blk.Body)
	}
	if blk.Body[0] != "thinking deeply..." {
		t.Errorf("expected line 1 %q, got %q", "thinking deeply...", blk.Body[0])
	}
}

func TestSetSize_ReflowsProseMarkdownBlocks(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	// Add long markdown text when width is uninitialized (width 0 -> prose width 20)
	longText := "This is a long sentence that should wrap to many lines when narrow and fewer lines when wide."
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindTextEnd,
		Body: uievent.TextEndBody{Text: longText},
	})

	initialLineCount := len(m.Blocks()[0].Body)

	// Now resize to wide width (120)
	m.SetSize(120, 24)
	wideLineCount := len(m.Blocks()[0].Body)

	if wideLineCount >= initialLineCount {
		t.Errorf("expected reflowed markdown to have fewer lines at width 120 (%d) than initial width 20 (%d)", wideLineCount, initialLineCount)
	}
}

func TestSingleEventReasoningHydrationPreservesBody(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetSize(80, 24)

	// Single atomic event as sent by history replay
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindReasoning,
		Body: uievent.ReasoningDeltaBody{Text: "step 1: read file\nstep 2: write code", WordCount: 8},
	})

	if len(m.Blocks()) != 1 {
		t.Fatalf("expected 1 block, got %d", len(m.Blocks()))
	}
	blk := m.Blocks()[0]
	if blk.Kind != uievent.KindReasoning {
		t.Fatalf("expected KindReasoning, got %v", blk.Kind)
	}
	if len(blk.Body) != 2 {
		t.Fatalf("expected 2 lines in body, got %d: %v", len(blk.Body), blk.Body)
	}
	if blk.Body[0] != "step 1: read file" {
		t.Errorf("expected line 1 'step 1: read file', got %q", blk.Body[0])
	}
}

// TestToolEndDurationUsesTheSharedLadder pins transcript-polish.md R5
// in the transcript: both tool-end duration sites - the result path and
// the diff path - go through render.FormatElapsed, so the header states
// "4.1s", "23.5s", "1m 30s", never raw milliseconds above a second.
func TestToolEndDurationUsesTheSharedLadder(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "frobnicate", OK: true, Result: "fine", DurationMS: 4100}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c2", Name: "subagent", OK: true, Result: "done", DurationMS: 23500}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c3", Name: "edit", OK: true, Diff: sampleDiff(), DurationMS: 90000}})
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c4", Name: "read_file", OK: true, Result: "fine", DurationMS: 12}})

	got := ansi.Strip(m.Dump())
	for _, want := range []string{"4.1s", "23.5s", "1m 30s", "12ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("the transcript is missing duration %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"4100ms", "23500ms", "90000ms"} {
		if strings.Contains(got, banned) {
			t.Errorf("raw millisecond duration %q reached the header:\n%s", banned, got)
		}
	}
}

// TestUnknownToolDoesNotDuplicateTheFirstBodyLine pins
// transcript-polish.md R7: on the direct tool.end push path with no
// prior live block, a tool the formatter does not know used to copy
// body line 1 into the header detail, printing it twice. The header
// keeps the tool name with an empty detail; the body carries every line
// exactly once.
func TestUnknownToolDoesNotDuplicateTheFirstBodyLine(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{ToolCallID: "c1", Name: "made_up_tool", OK: true,
			Result: "first line\nsecond line", DurationMS: 5}})

	header := ansi.Strip(strings.SplitN(m.Dump(), "\n", 2)[0])
	if !strings.Contains(header, "made_up_tool") {
		t.Errorf("the header must keep the tool name, got %q", header)
	}
	if strings.Contains(header, "first line") {
		t.Errorf("the header repeats body line 1: %q", header)
	}
	dump := ansi.Strip(m.Dump())
	if c := strings.Count(dump, "first line"); c != 1 {
		t.Errorf("body line 1 appears %d times, want 1:\n%s", c, dump)
	}
	if c := strings.Count(dump, "second line"); c != 1 {
		t.Errorf("body line 2 appears %d times, want 1:\n%s", c, dump)
	}
}

// TestUsageRendersAsAFooterLine pins transcript-polish.md R6: usage is
// one dim, header-less prose footer line in the shared meta grammar -
// grouped token counts and cost to two decimals - and it keeps the raw
// payload so a theme change can restyle it.
func TestUsageRendersAsAFooterLine(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierASCII)
	m.SetSize(80, 40)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindUsage, Body: uievent.UsageBody{
		InputTokens: 1284, OutputTokens: 2940, CachedTokens: 340, CostUSD: 0.041,
	}})

	blocks := m.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	b := blocks[0]
	if !b.Prose {
		t.Error("the usage footer must be prose: no header, no marker")
	}
	if b.Usage == nil {
		t.Fatal("the raw usage payload must be preserved for restyle")
	}
	row := ansi.Strip(strings.SplitN(b.Render(th, theme.TierASCII, 80), "\n", 2)[0])
	if want := "1,284 in  2,940 out  340 cached  $0.04"; row != want {
		t.Errorf("usage footer = %q, want %q", row, want)
	}
}

// TestSetThemeRestylesTheUsageFooter mirrors the plan-rebuild check: the
// footer is styled at push time, so restyle must rebuild it from the
// preserved payload, and the fact must survive the rebuild.
func TestSetThemeRestylesTheUsageFooter(t *testing.T) {
	// mivia-light repeats mivia-dark's fg-subtle truecolour, so the
	// rebuild check needs a theme whose subtle colour differs.
	dark, light := loadTheme(t), namedTheme(t, "mivia-high-contrast")
	m := New(dark, theme.TierTrueColor)
	m.SetSize(80, 24)
	m, _ = m.HandleEvent(uievent.Event{Kind: uievent.KindUsage, Body: uievent.UsageBody{
		InputTokens: 1284, OutputTokens: 2940,
	}})
	before := m.Blocks()[0].Body[0]
	m.SetTheme(light, theme.TierTrueColor)
	after := m.Blocks()[0].Body[0]
	if before == after {
		t.Error("SetTheme left the usage footer on the previous theme's colours")
	}
	if !strings.Contains(ansi.Strip(after), "1,284 in") {
		t.Errorf("the footer lost its facts on rebuild: %q", ansi.Strip(after))
	}
}

func TestFlushPendingReasoningCommitsAsReasoningBlock(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetSize(80, 24)

	// Stream reasoning delta chunk without terminal WordCount event
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindReasoning,
		Body: uievent.ReasoningDeltaBody{Text: "pondering solution..."},
	})
	// Followed by a tool start which triggers flushPending()
	m, _ = m.HandleEvent(uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{ToolCallID: "call_1", Name: "grep"},
	})

	blocks := m.Blocks()
	if len(blocks) < 1 {
		t.Fatalf("expected at least 1 block, got %d", len(blocks))
	}
	if blocks[0].Kind != uievent.KindReasoning {
		t.Fatalf("expected flushed block to be KindReasoning, got %v", blocks[0].Kind)
	}
	if len(blocks[0].Body) == 0 || blocks[0].Body[0] != "pondering solution..." {
		t.Errorf("expected body 'pondering solution...', got %v", blocks[0].Body)
	}
}
