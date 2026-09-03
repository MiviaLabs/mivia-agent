package conversation

// Subagent observation for the sidebar: how dispatch, progress and
// history events become subagentRow entries and statuses.

import (
	"slices"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func matchesAgentID(aID, id string) bool {
	if aID == id {
		return true
	}
	if aID == "" || id == "" {
		return false
	}
	if idx := strings.Index(aID, ":"); idx >= 0 {
		if aID[idx+1:] == id {
			return true
		}
	}
	if idx := strings.Index(id, ":"); idx >= 0 {
		if id[idx+1:] == aID {
			return true
		}
	}
	return false
}

// observeAgentStart records or updates one subagent's running status. A
// start means a NEW task under an old id - not history. Leaving it
// terminal would badge a genuinely running dispatch as already finished.
// Start events carry no group/call identity that could distinguish a
// genuinely out-of-order start arriving after its own dispatch's end;
// resetting is the lesser evil - worst case a finished row briefly shows
// running until its end event re-terminates it.
func (p *panel) observeAgentStart(id, name string) {
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			if name != "" {
				p.agents[i].Name = name
			}
			if a.Status == "" || a.Status == "pending" || isTerminalStatus(a.Status) {
				p.agents[i].Status = "running"
				// A (re)created row is a NEW run under a reused id: anchor
				// its stall clock and its elapsed-time clock now, so the
				// fresh row never renders instantly "stalled", nor reports
				// the elapsed time of the run that already ended.
				now := time.Now()
				p.agents[i].LastProgress = now
				p.agents[i].StartedAt = now
			}
			p.rebindIfOpen()
			return
		}
	}
	now := time.Now()
	p.agents = append(p.agents, subagentRow{ID: id, Name: name, Status: "running", LastProgress: now, StartedAt: now})
	p.rebindIfOpen()
}

// observeAgentEnd updates a tracked subagent's terminal state upon completion or failure.
func (p *panel) observeAgentEnd(id string, ok bool) {
	status := "completed"
	if !ok {
		status = "failed"
	}
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			p.agents[i].Status = status
			p.agents[i].LastProgress = time.Now()
			p.rebindIfOpen()
			return
		}
	}
}

// observeAgentGroupStart registers a dispatch_tasks call's fanned-out
// per-task ids as one running row each - instead of observeAgentStart's
// single row for the whole call - and remembers the group under callID so
// observeAgentGroupEnd can resolve every member's terminal status when the
// outer call completes.
func (p *panel) observeAgentGroupStart(callID string, ids []string, names map[string]string) {
	if p.dispatchGroups == nil {
		p.dispatchGroups = map[string][]string{}
	}
	p.dispatchGroups[callID] = ids
	for _, id := range ids {
		name := ""
		if names != nil {
			name = names[id]
			if name == "" {
				prefix := callID + ":"
				rawID := strings.TrimPrefix(id, prefix)
				name = names[rawID]
			}
		}
		p.observeAgentStart(id, name)
	}
}

// observeAgentGroupEnd resolves a dispatch_tasks group's per-task terminal
// status from statuses (task id -> status, parsed from the tool's own JSON
// result), falling back to ok for any member statuses does not cover. A
// no-op when callID names no tracked group (the ordinary single-row path
// handles it instead).
func (p *panel) observeAgentGroupEnd(callID string, statuses map[string]string, ok bool) {
	ids, found := p.dispatchGroups[callID]
	if !found {
		return
	}
	delete(p.dispatchGroups, callID)
	prefix := callID + ":"
	for _, id := range ids {
		rawID := strings.TrimPrefix(id, prefix)
		status := statuses[id]
		if status == "" {
			status = statuses[rawID]
		}
		if status != "" {
			p.setAgentStatus(id, status)
			continue
		}
		p.observeAgentEnd(id, ok)
	}
}

// setAgentStatus overwrites one tracked subagent's status verbatim - unlike
// observeAgentEnd, which only ever writes "completed" or "failed".
func (p *panel) setAgentStatus(id, status string) {
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			p.agents[i].Status = status
			p.agents[i].LastProgress = time.Now()
			p.rebindIfOpen()
			return
		}
	}
}

// isDispatchGroup reports whether callID names a tracked dispatch_tasks
// group, so the caller can choose the group-aware end path over the
// ordinary single-row one.
func (p panel) isDispatchGroup(callID string) bool {
	_, found := p.dispatchGroups[callID]
	return found
}

// reconcileTerminal transitions all non-terminal subagents to a terminal state
// when a turn ends without explicit tool end events (cancellation, error, interrupt).
func (p *panel) reconcileTerminal(reason string) {
	status := statusCancelled
	switch reason {
	case "error", "failed":
		status = statusFailed
	case "interrupted":
		status = statusInterrupted
	case "completed":
		status = statusCompleted
	}
	p.agents = slices.Clone(p.agents)
	changed := false
	for i, a := range p.agents {
		if isNonTerminalStatus(a.Status) {
			p.agents[i].Status = status
			p.agents[i].LastProgress = time.Now()
			changed = true
		}
	}
	if changed {
		p.rebindIfOpen()
	}
}

// observeAgentHistory idempotently registers or updates a subagent from replayed history.
func (p *panel) observeAgentHistory(id, status string) {
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			if isNonTerminalStatus(a.Status) {
				p.agents[i].Status = status
			}
			p.rebindIfOpen()
			return
		}
	}
	p.agents = append(p.agents, subagentRow{ID: id, Status: status})
	p.rebindIfOpen()
}

// activeAgentCount returns the count of currently running/pending subagents.
func (p panel) activeAgentCount() int {
	count := 0
	for _, a := range p.agents {
		if isNonTerminalStatus(a.Status) {
			count++
		}
	}
	return count
}

// observeAgent records progress for a subagent. It preserves the subagent's
// starting state and name, updates steps/toolcalls, and advances the stall clock
// only when real forward progress occurs.
func (p *panel) observeAgent(id string, pr *uievent.Progress) {
	log := slices.Clone(pr.Log)
	p.agents = slices.Clone(p.agents)
	for i, a := range p.agents {
		if matchesAgentID(a.ID, id) {
			if isTerminalStatus(a.Status) && !isTerminalStatus(pr.Status) {
				return
			}
			row := a
			if pr.Status != "" {
				row.Status = pr.Status
			}
			if pr.Step > 0 {
				row.Step = pr.Step
			}
			if pr.TotalSteps > 0 {
				row.Total = pr.TotalSteps
			}
			if pr.ToolCalls > 0 {
				row.ToolCalls = pr.ToolCalls
			}
			if len(log) > 0 {
				combinedLog := make([]string, 0, len(a.Log)+len(log))
				combinedLog = append(combinedLog, a.Log...)
				combinedLog = append(combinedLog, log...)
				row.Log = combinedLog
			}
			if progressAdvances(a, row) {
				row.LastProgress = time.Now()
			}
			p.agents[i] = row
			p.rebindIfOpen()
			return
		}
	}
	now := time.Now()
	row := subagentRow{
		ID:           id,
		Status:       pr.Status,
		Step:         pr.Step,
		Total:        pr.TotalSteps,
		ToolCalls:    pr.ToolCalls,
		Log:          log,
		LastProgress: now,
		StartedAt:    now,
	}
	p.agents = append(p.agents, row)
	p.rebindIfOpen()
}

// openPanel shows the panel with focus in its list, refreshing the list
// over everything observed while it was closed, and lands the cursor on
