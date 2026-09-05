package uiadapter

// Per-entry agent-selection state lookup and registration: split from
// session_pool.go to keep it under the go-structure soft cap. See
// SessionPool.agentStates for why each pooled entry needs its own fork.

import (
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
)

// AgentState returns the per-entry agent-selection state for a pooled
// session id: agentState itself for the launch entry, a private Fork for
// every later entry, or nil if the id was never registered. Callers
// (CommandRunner.SetActiveSession) use this to swap the active agent
// context alongside the active session, instead of leaving every pooled
// session mutate the one shared instance.
func (p *SessionPool) AgentState(id string) *cliagents.AgentSessionState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.agentStates[id]
}

// forkEntryStateLocked forks the pool's base agent state for a session the
// pool is constructing, BEFORE its runtime closures are wired: the deferred
// tool widener and the /model binding factory must close over this fork,
// never over the shared base (bug-audit "widener and binding factory bound
// to the shared base state"). Nil when the pool has no base state - the
// "tools/agent switching unavailable" case NewCommandRunner's nil-state
// callers already handle. Callers hold p.mu.
func (p *SessionPool) forkEntryStateLocked() *cliagents.AgentSessionState {
	if p.agentState == nil {
		return nil
	}
	return p.agentState.Fork()
}

// bindEntryStateLocked registers state as the private agent state of the
// pooled entry id. A nil state registers nothing. Callers hold p.mu.
func (p *SessionPool) bindEntryStateLocked(id string, state *cliagents.AgentSessionState) {
	if state == nil || id == "" {
		return
	}
	if p.agentStates == nil {
		p.agentStates = make(map[string]*cliagents.AgentSessionState)
	}
	p.agentStates[id] = state
}

// EnsureAgentState returns the per-entry agent state for id, forking one
// from the base and registering it when the entry has none yet. A session
// that reached the pool through a path that never registered a fork must
// still get its OWN state on activation, not silently keep another
// conversation's (bug-audit "switching sessions stops working"). Nil only
// when the pool has no base state at all.
func (p *SessionPool) EnsureAgentState(id string) *cliagents.AgentSessionState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if state := p.agentStates[id]; state != nil {
		return state
	}
	state := p.forkEntryStateLocked()
	p.bindEntryStateLocked(id, state)
	return state
}
