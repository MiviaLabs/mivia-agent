package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestChildTreeRendersUnderTheDispatchRow is C7's screen-wiring contract
// (docs/design/chat-tui-crush-comparison.md §3 C7, Phase 8 slice 3): on each
// subagent progress event and on the dispatch call's own end, the screen
// reads the thread history it already owns and pushes the child calls into
// the transcript - without arming any clock - so the dispatch row shows what
// the batch DID, with the outcome on every child row.
func TestChildTreeRendersUnderTheDispatchRow(t *testing.T) {
	// task-a's thread made two child calls, one of which failed: exactly
	// the script the phase prompt pins ("+' and 'x' child rows").
	taskA := &scriptedThread{history: []ports.Message{
		{Role: "user", Text: "do the work"},
		{Role: "assistant", ToolCalls: []ports.ToolCall{
			{ID: "c1", Name: "read_file", Arguments: `{"path":"a.go"}`, Output: "48 lines", OK: true},
			{ID: "c2", Name: "edit", Arguments: `{"path":"b.go"}`, Output: "error: permission denied", OK: false},
		}},
	}}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.threads = stubThreads{"dispatch_tasks-call-1:task-a": taskA}
	s.active = fakeHandle{id: "t1"}
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	s = next.(Screen)

	step := func(ev uievent.Event) {
		t.Helper()
		next, _ := s.Update(uievent.EventMsg{Event: ev})
		s = next.(Screen)
	}
	// Three settled siblings that are ALLOWED to fold, then the dispatch
	// batch whose row must stay its own.
	for i, tc := range []struct{ name, path, result string }{
		{"read_file", "z.go", "3 lines"},
		{"edit", "z.go", "patched"},
		{"run_command", "", "exit=0"},
	} {
		var args map[string]any
		if tc.path != "" {
			args = map[string]any{"path": tc.path}
		}
		step(uievent.Event{Kind: uievent.KindToolStart,
			Body: uievent.ToolStartBody{ToolCallID: string(rune('a' + i)), Name: tc.name, Args: args}})
		step(uievent.Event{Kind: uievent.KindToolEnd,
			Body: uievent.ToolEndBody{ToolCallID: string(rune('a' + i)), Name: tc.name, OK: true, Result: tc.result}})
	}
	step(uievent.Event{Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{
			ToolCallID: "dispatch_tasks-call-1", Name: "dispatch_tasks",
			Args: map[string]any{"tasks": []any{
				map[string]any{"id": "task-a", "prompt": "a"},
				map[string]any{"id": "task-b", "prompt": "b"},
			}},
		}})
	// The live signal: task-a's progress arrives long before the batch ends.
	step(uievent.Event{Kind: uievent.KindToolOutput,
		Body: uievent.ToolOutputBody{
			ToolCallID: "dispatch_tasks-call-1:task-a",
			Progress:   &uievent.Progress{Status: "running", Step: 2, ToolCalls: 2},
		}})
	step(uievent.Event{Kind: uievent.KindToolEnd,
		Body: uievent.ToolEndBody{
			ToolCallID: "dispatch_tasks-call-1", Name: "dispatch_tasks", OK: true,
			Result: `[{"task_id":"dispatch_tasks-call-1:task-a","status":"completed"},` +
				`{"task_id":"dispatch_tasks-call-1:task-b","status":"completed"}]`,
		}})

	dump := ansi.Strip(s.transcript.Dump())
	for _, row := range []string{"+ read_file a.go", "x edit b.go"} {
		if c := strings.Count(dump, row); c != 1 {
			t.Errorf("child row %q occurrence=%d, want exactly once:\n%s", row, c, dump)
		}
	}

	view := ansi.Strip(s.View())
	if !strings.Contains(view, "> work") {
		t.Errorf("the three settled siblings must fold into the work row:\n%s", view)
	}
	if !strings.Contains(view, "dispatch_tasks") {
		t.Errorf("the dispatch block lost its own row - it folded with the siblings:\n%s", view)
	}
	if !strings.Contains(view, "+ read_file a.go") || !strings.Contains(view, "x edit b.go") {
		t.Errorf("the child tree is not visible in the live view:\n%s", view)
	}
}
