// subagentTracker aggregates attributed subagent events into per-agent rows.
// It is the data spine for the fleet box and the per-agent turn ledger: a
// pure state machine - feed it with Apply, read it with Rows. It renders
// nothing and holds no locks; the TUI update loop owns it. All methods are
// nil-receiver safe so models built without a tracker stay inert.
package cli

import (
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// subagentRun is the aggregated view of one subagent's activity.
type subagentRun struct {
	TaskID     string
	Name       string
	Depth      int
	Started    time.Time
	LastSeen   time.Time
	LastTool   string // most recent nested tool name
	LastDetail string // most recent detail/heartbeat text
	ToolsOpen  int
	ToolsDone  int
}

type subagentTracker struct {
	order []string
	runs  map[string]*subagentRun
}

func newSubagentTracker() *subagentTracker {
	return &subagentTracker{runs: map[string]*subagentRun{}}
}

// Apply folds one bus event into the tracker. Only events attributed to an
// agent (AgentTask set) register; anything else is ignored rather than
// misfiled. Reports whether state changed.
func (t *subagentTracker) Apply(ev events.Event, now time.Time) bool {
	if t == nil || ev.AgentTask == "" {
		return false
	}
	run, ok := t.runs[ev.AgentTask]
	if !ok {
		run = &subagentRun{
			TaskID:  ev.AgentTask,
			Name:    ev.AgentName,
			Depth:   ev.AgentDepth,
			Started: now,
		}
		t.runs[ev.AgentTask] = run
		t.order = append(t.order, ev.AgentTask)
	}
	run.LastSeen = now
	switch ev.Kind {
	case events.KindSubagentStart:
		run.ToolsOpen++
		if ev.Name != "" {
			run.LastTool = ev.Name
		}
		if ev.Detail != "" {
			run.LastDetail = ev.Detail
		}
	case events.KindSubagentEnd:
		if run.ToolsOpen > 0 {
			run.ToolsOpen--
		}
		run.ToolsDone++
		if ev.Name != "" {
			run.LastTool = ev.Name
		}
	case events.KindSubagentHeartbeat:
		if ev.Detail != "" {
			run.LastDetail = ev.Detail
		}
	default:
		return false
	}
	return true
}

// Rows returns the runs in stable first-seen order.
func (t *subagentTracker) Rows() []subagentRun {
	if t == nil || len(t.order) == 0 {
		return nil
	}
	rows := make([]subagentRun, 0, len(t.order))
	for _, id := range t.order {
		if run, ok := t.runs[id]; ok {
			rows = append(rows, *run)
		}
	}
	return rows
}

// Active counts runs with open nested tools.
func (t *subagentTracker) Active() int {
	if t == nil {
		return 0
	}
	n := 0
	for _, run := range t.runs {
		if run.ToolsOpen > 0 {
			n++
		}
	}
	return n
}

// Reset clears all runs (called when a new turn starts).
func (t *subagentTracker) Reset() {
	if t == nil {
		return
	}
	t.order = nil
	t.runs = map[string]*subagentRun{}
}
