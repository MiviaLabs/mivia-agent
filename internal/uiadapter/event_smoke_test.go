package uiadapter_test

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/uiadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// TestTranslateSmoke_DispatchTaskHeartbeatsCoalesce is the offline smoke
// test ADLC mandates for UI-ship phases. It drives the translated stream
// with a realistic dispatch_tasks-shaped event sequence (two subagents,
// interleaved heartbeats, staggered completion) using NO live credentials,
// and asserts PER-KIND COUNTS plus the coalescing shape renderers rely on:
//
//   - exactly one tool.start and one terminal progress per task row,
//   - every heartbeat of a task rides the SAME ToolCallID key, so a
//     renderer that replaces per-key state shows the LAST detail only and
//     the row count never grows with heartbeat volume,
//   - no notice/text content from any heartbeat reaches the stream.
func TestTranslateSmoke_DispatchTaskHeartbeatsCoalesce(t *testing.T) {
	taskA := agent.EventOrigin{TaskID: "task-a", Agent: "reviewer", TaskDescription: "structural review"}
	taskB := agent.EventOrigin{TaskID: "task-b", Agent: "auditor", TaskDescription: "correctness audit"}

	seq := []agent.Event{
		{Kind: agent.EventSubagentStart, ToolCallID: "tc-a", Name: "delegate", Origin: taskA},
		{Kind: agent.EventSubagentStart, ToolCallID: "tc-b", Name: "delegate", Origin: taskB},
		{Kind: agent.EventSubagentHeartbeat, Detail: "a1 reading policy", Origin: taskA},
		{Kind: agent.EventSubagentHeartbeat, Detail: "b1 grepping", Origin: taskB},
		{Kind: agent.EventSubagentHeartbeat, Detail: "a2 reading pool", Origin: taskA},
		{Kind: agent.EventSubagentHeartbeat, Detail: "b2 still grepping", Origin: taskB},
		{Kind: agent.EventSubagentDone, Origin: taskA},
		{Kind: agent.EventSubagentEnd, ToolCallID: "tc-a", Name: "delegate", Detail: "completed", Output: "done"},
		{Kind: agent.EventSubagentHeartbeat, Detail: "b3 last one", Origin: taskB},
		{Kind: agent.EventSubagentDone, Origin: taskB},
		{Kind: agent.EventSubagentEnd, ToolCallID: "tc-b", Name: "delegate", Detail: "completed", Output: "clean"},
	}

	starts, ends, doneProgress, hbByTask := 0, 0, map[string]int{}, map[string]int{}
	lastDetail := map[string]string{}
	notices := 0
	for _, ev := range seq {
		for _, out := range uiadapter.TranslateEvent(ev) {
			switch b := out.Body.(type) {
			case uievent.ToolStartBody:
				starts++
			case uievent.ToolEndBody:
				ends++
			case uievent.ToolOutputBody:
				if b.Progress == nil {
					continue
				}
				switch b.Progress.Status {
				case "running":
					hbByTask[b.ToolCallID]++
					if len(b.Progress.Log) > 0 {
						lastDetail[b.ToolCallID] = b.Progress.Log[0]
					}
				case "completed":
					doneProgress[b.ToolCallID]++
				}
			case uievent.NoticeBody:
				if strings.Contains(b.Text, "tick") || strings.Contains(b.Text, "reading") || strings.Contains(b.Text, "grepping") {
					notices++
				}
			}
		}
	}

	if starts != 2 {
		t.Errorf("tool.start count = %d, want 2 (one per dispatched task)", starts)
	}
	if ends != 2 {
		t.Errorf("tool.end count = %d, want 2", ends)
	}
	if notices != 0 {
		t.Errorf("heartbeat details leaked into %d notice lines, want 0", notices)
	}
	if n := hbByTask["task-a"]; n != 2 {
		t.Errorf("task-a heartbeat count = %d, want 2", n)
	}
	if n := hbByTask["task-b"]; n != 3 {
		t.Errorf("task-b heartbeat count = %d, want 3", n)
	}
	if got := lastDetail["task-a"]; got != "a2 reading pool" {
		t.Errorf("task-a last detail = %q, want latest heartbeat detail", got)
	}
	if got := lastDetail["task-b"]; got != "b3 last one" {
		t.Errorf("task-b last detail = %q, want latest heartbeat detail", got)
	}
	for _, id := range []string{"task-a", "task-b"} {
		if n := doneProgress[id]; n != 1 {
			t.Errorf("task %s terminal progress count = %d, want exactly 1", id, n)
		}
	}
	if len(doneProgress) != 2 {
		t.Errorf("terminal progress rows = %d, want 2 distinct task rows", len(doneProgress))
	}
}
