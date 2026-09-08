package uiadapter

// ApplySyncOpts: split from session_pool.go to keep it under the
// go-structure hard cap. Mirrors session_pool_agentstate.go's
// ApplyApprovalDefault in shape and rationale - both are pool-wide
// live re-arm fan-outs triggered by a Settings -> General edit.

import "github.com/MiviaLabs/mivia-agent/internal/chatsync"

// ApplySyncOpts flips the three [sync] opt-out flags on every
// attached SyncSession, in place. It is the live re-arm seam for the
// General settings toggles: a Settings -> General operator action that
// toggles include_thinking, include_tool_io, or stream_assistant
// fires the SettingsStore.syncOptsNotifier, which the launcher wires
// to this method.
//
// Locking: the snapshot of attached sessions is taken under p.mu; the
// per-session SetSyncOpts call runs OUTSIDE p.mu (matching
// ReattachSyncAfterLogin's fan-out discipline) because
// chatsync.SyncSession.SetSyncOpts takes its own lock and the round
// trip is bounded but non-zero across many pooled sessions. A session
// added during the fan-out simply misses the toggle - the next toggle
// from the operator picks it up; we do not retry.
//
// No return value, matching ApplyApprovalDefault's fire-and-forget
// shape: a logged-out operator, or a pool with no attached
// SyncSession, has nothing to re-arm, and the toggle still takes
// effect on disk via applyGeneral's persist half - the absence of
// live re-arm is silent and correct, not an error a caller needs to
// observe.
func (p *SessionPool) ApplySyncOpts(includeThinking, includeToolIO, streamAssistant bool) {
	p.mu.Lock()
	attached := make([]*chatsync.SyncSession, 0, len(p.syncSessions))
	for _, ss := range p.syncSessions {
		if ss != nil {
			attached = append(attached, ss)
		}
	}
	p.mu.Unlock()

	for _, ss := range attached {
		ss.SetSyncOpts(includeThinking, includeToolIO, streamAssistant)
	}
}
