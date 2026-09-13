package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestConversationScreen_StreamingEventDeduplicationSmoke verifies that a realistic
// streaming turn with intermediate notices and usage does not duplicate assistant
// or user text in the transcript or screen view.
func TestConversationScreen_StreamingEventDeduplicationSmoke(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	s := New(themes[0], theme.TierASCII, themes, nil, nil, 80, fixedNow)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)

	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	events := []uievent.Event{
		{Kind: uievent.KindTurnStart, Seq: 1, At: at, Body: uievent.TurnStartBody{Input: "check repo health"}},
		{Kind: uievent.KindNotice, Seq: 2, At: at, Body: uievent.NoticeBody{Text: "iteration 1"}},
		{Kind: uievent.KindTextDelta, Seq: 3, At: at, Body: uievent.TextDeltaBody{Text: "Everything is clean and healthy."}},
		{Kind: uievent.KindNotice, Seq: 4, At: at, Body: uievent.NoticeBody{Text: "prompt cache: 2000/2000 tokens (100%)"}},
		{Kind: uievent.KindUsage, Seq: 5, At: at, Body: uievent.UsageBody{InputTokens: 2000, OutputTokens: 50, CachedTokens: 2000, CostUSD: 0.005}},
		{Kind: uievent.KindTextEnd, Seq: 6, At: at, Body: uievent.TextEndBody{Text: "Everything is clean and healthy."}},
		{Kind: uievent.KindTurnEnd, Seq: 7, At: at, Body: uievent.TurnEndBody{Reason: "completed"}},
	}

	for _, ev := range events {
		next, _ = s.Update(uievent.EventMsg{Event: ev})
		s = next.(Screen)
	}

	dump := ansi.Strip(s.transcript.Dump())

	// Assistant response must occur exactly once
	if c := strings.Count(dump, "Everything is clean and healthy."); c != 1 {
		t.Errorf("assistant text occurrence count=%d, want 1 in transcript dump:\n%s", c, dump)
	}

	// User input must occur exactly once
	if c := strings.Count(dump, "check repo health"); c != 1 {
		t.Errorf("user input occurrence count=%d, want 1 in transcript dump:\n%s", c, dump)
	}

	// Notices must occur exactly once
	for _, notice := range []string{"iteration 1", "prompt cache: 2000/2000 tokens (100%)"} {
		if c := strings.Count(dump, notice); c != 1 {
			t.Errorf("notice %q count=%d, want 1 in transcript dump:\n%s", notice, c, dump)
		}
	}

	// Full rendered view must also contain the assistant text
	view := ansi.Strip(s.View())
	if !strings.Contains(view, "Everything is clean and healthy.") {
		t.Errorf("rendered view missing assistant text:\n%s", view)
	}
}

// TestConversationScreen_ChildTreeSmoke extends the offline smoke surface
// with C7's user-visible behavior (docs/design/chat-tui-crush-comparison.md
// §3 C7): a dispatch_tasks batch shows its settled child calls once under
// its own row, and the row is NOT folded into the work-run summary - the
// fold would hide exactly the tree the feature exists to show. Mirrors what
// manual acceptance does: dispatch, progress, settle, read the screen.
func TestConversationScreen_ChildTreeSmoke(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.threads = stubThreads{"smoke-call:task-a": &scriptedThread{history: []ports.Message{
		{Role: "assistant", ToolCalls: []ports.ToolCall{
			{ID: "k1", Name: "read_file", Arguments: `{"path":"a.go"}`, Output: "48 lines", OK: true},
			{ID: "k2", Name: "edit", Arguments: `{"path":"b.go"}`, Output: "error: denied", OK: false},
		}},
	}}}
	s.active = fakeHandle{id: "t1"}
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	s = next.(Screen)

	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	events := []uievent.Event{
		{Kind: uievent.KindTurnStart, Seq: 1, At: at, Body: uievent.TurnStartBody{Input: "dispatch the batch"}},
		// Three settled siblings that MAY fold.
		{Kind: uievent.KindToolStart, Seq: 2, At: at, Body: uievent.ToolStartBody{ToolCallID: "s1", Name: "read_file", Args: map[string]any{"path": "z.go"}}},
		{Kind: uievent.KindToolEnd, Seq: 3, At: at, Body: uievent.ToolEndBody{ToolCallID: "s1", Name: "read_file", OK: true, Result: "3 lines"}},
		{Kind: uievent.KindToolStart, Seq: 4, At: at, Body: uievent.ToolStartBody{ToolCallID: "s2", Name: "edit", Args: map[string]any{"path": "z.go"}}},
		{Kind: uievent.KindToolEnd, Seq: 5, At: at, Body: uievent.ToolEndBody{ToolCallID: "s2", Name: "edit", OK: true, Result: "patched"}},
		{Kind: uievent.KindToolStart, Seq: 6, At: at, Body: uievent.ToolStartBody{ToolCallID: "s3", Name: "run_command", Args: map[string]any{"command": "go vet ./..."}}},
		{Kind: uievent.KindToolEnd, Seq: 7, At: at, Body: uievent.ToolEndBody{ToolCallID: "s3", Name: "run_command", OK: true, Result: "exit=0"}},
		// The batch: fan-out, one task's progress, then the batch settles.
		{Kind: uievent.KindToolStart, Seq: 8, At: at, Body: uievent.ToolStartBody{ToolCallID: "smoke-call", Name: "dispatch_tasks", Args: map[string]any{"tasks": []any{
			map[string]any{"id": "task-a", "prompt": "a"},
			map[string]any{"id": "task-b", "prompt": "b"},
		}}}},
		{Kind: uievent.KindToolOutput, Seq: 9, At: at, Body: uievent.ToolOutputBody{ToolCallID: "smoke-call:task-a",
			Progress: &uievent.Progress{Status: "running", Step: 2, ToolCalls: 2}}},
		{Kind: uievent.KindToolEnd, Seq: 10, At: at, Body: uievent.ToolEndBody{ToolCallID: "smoke-call", Name: "dispatch_tasks", OK: true,
			Result: `[{"task_id":"smoke-call:task-a","status":"completed"},{"task_id":"smoke-call:task-b","status":"completed"}]`}},
		{Kind: uievent.KindTurnEnd, Seq: 11, At: at, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
	for _, ev := range events {
		next, _ = s.Update(uievent.EventMsg{Event: ev})
		s = next.(Screen)
	}

	dump := ansi.Strip(s.transcript.Dump())
	for _, row := range []string{"+ read_file a.go", "x edit b.go"} {
		if c := strings.Count(dump, row); c != 1 {
			t.Errorf("smoke: child row %q appears %d times, want exactly once:\n%s", row, c, dump)
		}
	}
	view := ansi.Strip(s.View())
	if !strings.Contains(view, "> work") {
		t.Errorf("smoke: the settled siblings did not fold - the fixture does not mirror a real batch:\n%s", view)
	}
	if !strings.Contains(view, "dispatch_tasks") {
		t.Errorf("smoke: the parent dispatch row was folded away; its tree must keep the row alive:\n%s", view)
	}
	if !strings.Contains(view, "+ read_file a.go") || !strings.Contains(view, "x edit b.go") {
		t.Errorf("smoke: the child tree is not visible in the live view:\n%s", view)
	}
}

// TestConversationScreen_MultiTurnStreamingSmoke verifies that multiple sequential
// streaming turns render accurately without text leakage between turns.
func TestConversationScreen_MultiTurnStreamingSmoke(t *testing.T) {
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	s := New(themes[0], theme.TierASCII, themes, nil, nil, 80, fixedNow)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)

	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)

	// Turn 1
	turn1 := []uievent.Event{
		{Kind: uievent.KindTurnStart, Seq: 1, At: at, Body: uievent.TurnStartBody{Input: "first prompt"}},
		{Kind: uievent.KindTextDelta, Seq: 2, At: at, Body: uievent.TextDeltaBody{Text: "first reply"}},
		{Kind: uievent.KindTextEnd, Seq: 3, At: at, Body: uievent.TextEndBody{Text: "first reply"}},
		{Kind: uievent.KindTurnEnd, Seq: 4, At: at, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
	for _, ev := range turn1 {
		next, _ = s.Update(uievent.EventMsg{Event: ev})
		s = next.(Screen)
	}

	// Turn 2
	turn2 := []uievent.Event{
		{Kind: uievent.KindTurnStart, Seq: 5, At: at, Body: uievent.TurnStartBody{Input: "second prompt"}},
		{Kind: uievent.KindTextDelta, Seq: 6, At: at, Body: uievent.TextDeltaBody{Text: "second reply"}},
		{Kind: uievent.KindTextEnd, Seq: 7, At: at, Body: uievent.TextEndBody{Text: "second reply"}},
		{Kind: uievent.KindTurnEnd, Seq: 8, At: at, Body: uievent.TurnEndBody{Reason: "completed"}},
	}
	for _, ev := range turn2 {
		next, _ = s.Update(uievent.EventMsg{Event: ev})
		s = next.(Screen)
	}

	dump := ansi.Strip(s.transcript.Dump())

	if c := strings.Count(dump, "first prompt"); c != 1 {
		t.Errorf("first prompt count=%d, want 1", c)
	}
	if c := strings.Count(dump, "first reply"); c != 1 {
		t.Errorf("first reply count=%d, want 1", c)
	}
	if c := strings.Count(dump, "second prompt"); c != 1 {
		t.Errorf("second prompt count=%d, want 1", c)
	}
	if c := strings.Count(dump, "second reply"); c != 1 {
		t.Errorf("second reply count=%d, want 1", c)
	}
}
