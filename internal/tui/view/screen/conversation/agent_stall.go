// agent_stall.go holds the "stalled" display derivation for the files
// panel's subagent rows.
//
// The incident behind it: a subagent sat in "running" for over ten minutes
// after its final report was visible, because its provider connection
// trickled bytes. The heartbeats kept ticking with a frozen step count, and
// the row treated any heartbeat as proof of work, so nothing ever aged. The
// panel now keys its stall clock on forward motion: a heartbeat refreshes
// the row's LastProgress only when its step count changed, and any other
// progress or tool event refreshes it.
//
// "stalled" is a display state only. displayStatus derives it at render
// time; no code path stores it in a row. Stored statuses keep the old
// vocabulary, so terminal-status pinning (isTerminalStatus) is untouched,
// and any qualifying progress event flips the badge straight back to
// "running" on the next draw.
//
// Display lag is one render trigger: the badge appears on the next draw
// after the threshold is crossed - the next heartbeat, or the status line's
// existing spinner tick (statusline.TickMsg) that the conversation screen
// already repaints on. So a row can read "running" for up to one 30s
// heartbeat interval past uiStallThreshold.
package conversation

import "time"

// statusStalled is the derived badge for a non-terminal row whose last
// forward motion is older than the stall threshold. It is never stored.
const statusStalled = "stalled"

// uiStallThreshold is how long a non-terminal subagent row may sit without
// forward motion before the panel derives "stalled". panel.stallThreshold
// carries the value so tests can shrink it; a threshold of 0 or less turns
// the derivation off (a zero-value panel never derives "stalled").
const uiStallThreshold = 180 * time.Second

// displayStatus returns the status label the panel renders for one row:
// "stalled" when the row is non-terminal and has not moved forward within
// the stall threshold, otherwise the stored status.
//
// A row with no LastProgress anchor - a history-replayed row has none -
// keeps its stored status: absent evidence is not evidence of a stall.
// Terminal statuses always win: a finished run is never "stalled", however
// stale its last progress looks.
func (p panel) displayStatus(a subagentRow) string {
	if p.stallThreshold <= 0 || isTerminalStatus(a.Status) {
		return a.Status
	}
	if !a.LastProgress.IsZero() && time.Since(a.LastProgress) > p.stallThreshold {
		return statusStalled
	}
	return a.Status
}

// progressAdvances reports whether one progress update counts as forward
// motion for a row's stall clock. A terminal update always counts (a
// finished row never renders "stalled"). A non-terminal update counts only
// when it carries a step count that differs from the row's stored step:
// heartbeats tick on a fixed cadence even while nothing advances, and an
// unparseable count (Step 0, a raw loop step remap) is liveness without
// progress information - never step 0 of real work.
func progressAdvances(prev, next subagentRow) bool {
	if isTerminalStatus(next.Status) {
		return true
	}
	if next.Step > 0 && next.Step != prev.Step {
		return true
	}
	// A single step can carry many tool calls (several file reads before
	// the model's next full turn): Step alone would read that whole
	// stretch as frozen, risking a false "stalled" badge on a row that is
	// visibly making tool calls.
	return next.ToolCalls > 0 && next.ToolCalls != prev.ToolCalls
}
