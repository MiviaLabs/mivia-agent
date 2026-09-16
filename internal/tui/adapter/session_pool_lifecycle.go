package adapter

// Split from session_pool.go: the concurrency-critical session-release path
// (releaseSessionLease and ReleaseLeases), kept together because
// ReleaseLeases' statement order is load-bearing and must not scatter across
// files.
// INVARIANT: p.released is written here and read in four places - attachSyncLocked and StartBackgroundWatch (session_pool_sync.go), session_pool_worktree.go's refuseIfDrainedLocked, and workflow_notices.go. The latch must be stored under p.mu BEFORE the drain.

import (
	"context"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// releaseSessionLease is the per-session release hook ReleaseLeases invokes,
// a var so tests can record the visited sessions without arming real context
// heartbeats (the chat package owns that behavior's own tests).
var releaseSessionLease = func(ctx context.Context, sess *chat.Session) {
	sess.ReleaseContextLease(ctx)
}

// ReleaseLeases releases every pooled session's context lease. The TUI's
// return path must call this on shutdown: only the primary startup session
// is released by the chat surface's own defer, so without this every OTHER
// session resumed in the TUI kept a fresh lease behind (40s heartbeat, 2min
// TTL) and the next process's resume was refused with "live in another
// process" until the TTL ran out - the intermittent resume failure users
// read as a broken binary. Sessions registered under several ids (raw and
// canonical) are released once.
func (p *SessionPool) ReleaseLeases(ctx context.Context) {
	p.mu.Lock()
	// Latch BEFORE draining: ReattachSyncAfterLogin holds p.mu only for its
	// snapshot and re-locks per session, so a re-attach in flight while this
	// drains must see the latch in its own per-session critical section.
	p.released.Store(true)
	seen := make(map[*chat.Session]struct{}, len(p.sessions))
	distinct := make([]*chat.Session, 0, len(p.sessions))
	for _, sess := range p.sessions {
		if sess == nil {
			continue
		}
		if _, dup := seen[sess]; dup {
			continue
		}
		seen[sess] = struct{}{}
		distinct = append(distinct, sess)
	}
	// Paired with the owning session's EventBus, not collected alone: Stop
	// only drains SyncSession's own eventCh, not the bus subscription queue
	// feeding it (DC-30, .agents/quality/defect-taxonomy.md). A pooled
	// session that just ran a heavy-volume turn (subagent fan-out, or
	// [sync].stream_assistant = true) can still have events sitting
	// undelivered in that queue when the TUI quits; Flush must run first, on
	// THIS session's own bus, or the tail is silently lost on process exit.
	type pooledSync struct {
		bus *events.Bus
		ss  *chatsync.SyncSession
	}
	syncList := make([]pooledSync, 0, len(p.syncSessions))
	for id, ss := range p.syncSessions {
		if ss == nil {
			continue
		}
		var bus *events.Bus
		if sess := p.sessions[id]; sess != nil {
			bus = sess.EventBus
		}
		syncList = append(syncList, pooledSync{bus: bus, ss: ss})
	}
	p.syncSessions = make(map[string]*chatsync.SyncSession)
	// Drain busReleases alongside the sync-session teardown above: every
	// entry here came from a successful attachSyncLocked, so its lifetime
	// matches syncList's exactly. Missing entries (SessionBusRegistrar was
	// nil at attach time, or a session was never sync-attached at all) are
	// tolerated - the map simply has no release func for that id.
	busReleaseList := make([]func(), 0, len(p.busReleases))
	for _, release := range p.busReleases {
		if release != nil {
			busReleaseList = append(busReleaseList, release)
		}
	}
	p.busReleases = make(map[string]func())
	p.mu.Unlock()
	// Release outside p.mu: ReleaseContextLease must run lock-free (it joins
	// the heartbeat goroutine and issues a store write with its own timeout).
	for _, sess := range distinct {
		releaseSessionLease(ctx, sess)
	}
	for _, ps := range syncList {
		if ps.bus != nil {
			ps.bus.Flush()
		}
		_ = ps.ss.Stop(ctx)
	}
	for _, release := range busReleaseList {
		release()
	}
	if p.watcher != nil {
		p.watcher.Stop(ctx)
	}
}
