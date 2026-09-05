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

// registerForkedStateLocked forks the pool's base agent state for a newly
// created entry and registers it under id. No-op (no entry registered) when
// the pool has no base state - the same "tools/agent switching unavailable"
// case NewCommandRunner's nil-state callers already handle. Callers hold p.mu.
func (p *SessionPool) registerForkedStateLocked(id string) {
	if p.agentState == nil || id == "" {
		return
	}
	if p.agentStates == nil {
		p.agentStates = make(map[string]*cliagents.AgentSessionState)
	}
	p.agentStates[id] = p.agentState.Fork()
}
