package conversation

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/statusline"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// runCmd expands one Cmd the way the bubbletea runtime does (batches fan
// out, every leaf runs) and returns the Msgs it produced. Leaf Cmds that do
// not deliver within the budget (a blocked port read) are dropped: this
// helper only counts the spinner clock.
func expandTickCmd(cmd tea.Cmd, budget time.Duration) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, expandTickCmd(c, budget)...)
			}
			return out
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(budget):
		return nil
	}
}

// countTicks reports how many statusline.TickMsg a Cmd delivers, i.e. how
// many independent spinner clocks are alive in that branch of the loop.
func countTicks(cmd tea.Cmd) int {
	n := 0
	for _, msg := range expandTickCmd(cmd, 2*time.Second) {
		if _, ok := msg.(statusline.TickMsg); ok {
			n++
		}
	}
	return n
}

// TestSubagentProgressDoesNotMultiplySpinnerClocks pins that subagent
// progress events do not each arm their own spinner tick loop. The
// statusline clock is self-re-arming (every TickMsg returns the next
// TickCmd) and handleStatuslineTick keeps it alive while subagents run, so
// arming a second loop doubles the frame rate permanently: the sidebar and
// status bar animate N times too fast and the whole UI repaints N times per
// interval.
func TestSubagentProgressDoesNotMultiplySpinnerClocks(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)

	next, _ := s.Update(uievent.EventMsg{Event: uievent.Event{
		Kind: uievent.KindToolStart,
		Body: uievent.ToolStartBody{
			ToolCallID: "dispatch_tasks-call-1",
			Name:       "dispatch_tasks",
			Args: map[string]any{
				"tasks": []any{
					map[string]any{"id": "task-a", "prompt": "a"},
					map[string]any{"id": "task-b", "prompt": "b"},
				},
			},
		},
	}})
	scr := next.(Screen)

	// Several progress updates arrive while no turn statusline is active
	// (the outer turn ended, or none was started for this surface) - the
	// ordinary case for a long dispatch_tasks batch.
	live := 0
	for i := 0; i < 5; i++ {
		next, cmd := scr.Update(uievent.EventMsg{Event: uievent.Event{
			Kind: uievent.KindToolOutput,
			Body: uievent.ToolOutputBody{
				ToolCallID: "dispatch_tasks-call-1:task-a",
				Progress:   &uievent.Progress{Status: "running"},
			},
		}})
		scr = next.(Screen)
		live += countTicks(cmd)
	}

	if live > 1 {
		t.Fatalf("subagent progress armed %d concurrent spinner clocks, want at most 1", live)
	}

	// Whatever is alive must stay exactly one clock across a tick cycle.
	for cycle := 0; cycle < 3; cycle++ {
		total := 0
		for i := 0; i < live; i++ {
			nxt, cmd := scr.Update(statusline.TickMsg{})
			scr = nxt.(Screen)
			total += countTicks(cmd)
		}
		if total > live {
			t.Fatalf("cycle %d: spinner clocks grew from %d to %d", cycle, live, total)
		}
		live = total
	}
}
