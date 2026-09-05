package uiadapter

// Per-entry agent-selection state lookup and registration: split from
// session_pool.go to keep it under the go-structure soft cap. See
// SessionPool.agentStates for why each pooled entry needs its own fork.

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
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

// ApplyApprovalDefault re-arms the operator's approval posture on EVERY live
// pooled session, not just the focused one. Approval mode is an operator
// setting like full-disk access (see AgentSessionState.ApplyFullDisk's
// fan-out): a background or worktree session that kept the looser posture
// would go on executing tool calls - real edits and commands against a real
// checkout - under a policy the operator believes they revoked, while the UI
// shows the tightened value. Both the base and the live policy are set: the
// base is what a later /yolo toggle returns to.
func (p *SessionPool) ApplyApprovalDefault(mode string) {
	normalized := config.NormalizeDefaultMode(mode)
	if normalized == "" {
		return
	}
	p.mu.Lock()
	seen := make(map[*chat.Session]struct{}, len(p.sessions))
	live := make([]*chat.Session, 0, len(p.sessions))
	for _, sess := range p.sessions {
		if sess == nil {
			continue
		}
		if _, dup := seen[sess]; dup {
			continue
		}
		seen[sess] = struct{}{}
		live = append(live, sess)
	}
	p.mu.Unlock()
	for _, sess := range live {
		sess.SetBaseApprovalPolicy(normalized)
		sess.SetApprovalPolicy(normalized)
	}
}
