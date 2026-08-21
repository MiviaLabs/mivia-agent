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

// subagentTracker aggregates attributed subagent events into per-agent rows.
// It is the data spine for the fleet box and the per-agent turn ledger: a
// pure state machine - feed it with Apply, read it with Rows. It renders
// nothing and holds no locks; the TUI update loop owns it. All methods are
// nil-receiver safe so models built without a tracker stay inert.

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
	// Done is set by the run-level terminal event only. It is never inferred
	// from ToolsOpen == 0: an agent between two tool calls has no open tools
	// and is still running.
	Done bool
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
	switch ev.Kind {
	case events.KindSubagentStart, events.KindSubagentEnd,
		events.KindSubagentHeartbeat, events.KindSubagentDone:
	default:
		// Only the run lifecycle registers. Any other attributed event
		// (a nested loop's step or error) would open a row that no Done
		// event ever closes, and the live view would carry it all turn.
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
	case events.KindSubagentDone:
		// Terminal: the run's loop returned. Any tool still counted open is
		// one whose end event never arrived, so close it out rather than
		// leaving a phantom "+1 running" on a finished agent.
		run.Done = true
		run.ToolsOpen = 0
	default:
		return false
	}
	return true
}

// Rows returns every run of the turn, finished ones included, in stable
// first-seen order. This is turn history - the ctrl+g fleet detail and the
// diagnostics dialog want it. Live chrome wants ActiveRows.
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

// ActiveRows returns the runs that have not finished, in stable first-seen
// order. This is what the "now" panel and the fleet box render: a section
// named for what is happening right now must not carry finished work.
func (t *subagentTracker) ActiveRows() []subagentRun {
	if t == nil || len(t.order) == 0 {
		return nil
	}
	rows := make([]subagentRun, 0, len(t.order))
	for _, id := range t.order {
		if run, ok := t.runs[id]; ok && !run.Done {
			rows = append(rows, *run)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return rows
}

// Active counts runs that have not finished. It is the "n running" figure in
// the fleet box header and must agree with the rows rendered beneath it.
func (t *subagentTracker) Active() int {
	if t == nil {
		return 0
	}
	n := 0
	for _, run := range t.runs {
		if !run.Done {
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
