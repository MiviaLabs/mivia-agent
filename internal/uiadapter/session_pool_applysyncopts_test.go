package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// TestPoolApplySyncOptsFansOutToEveryAttachedSession pins the live
// re-arm fan-out: when the settings store fires the syncOptsNotifier,
// the pool must call SetSyncOpts on every attached SyncSession (not
// just one). The test seeds two attached SyncSession fixtures directly
// into p.syncSessions (bypassing attachSyncLocked, which needs a real
// remote) and asserts both projectors' opts flipped.
func TestPoolApplySyncOptsFansOutToEveryAttachedSession(t *testing.T) {
	res := &config.Resolved{ConfigPath: t.TempDir() + "/mivia.toml"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	pool := NewSessionPool(nil, res, state, false)

	// Two attached sessions, both with all gates OFF at start. Each is
	// constructed via the chatsync-package helper used by the SyncSession
	// unit tests; the fixtures here are not real running sessions, only
	// enough of the seam for ApplySyncOpts to reach the projector.
	pool.syncSessions["s1"] = chatsync.NewSyncSessionForTest(chatsync.ProjectorOptions{
		IncludeThinking: false,
		IncludeToolIO:   false,
		StreamAssistant: false,
	})
	pool.syncSessions["s2"] = chatsync.NewSyncSessionForTest(chatsync.ProjectorOptions{
		IncludeThinking: false,
		IncludeToolIO:   false,
		StreamAssistant: false,
	})

	pool.ApplySyncOpts(true, true, true)

	for id, ss := range pool.syncSessions {
		got := ss.ProjectorOptsForTest()
		if !got.IncludeThinking || !got.IncludeToolIO || !got.StreamAssistant {
			t.Errorf("session %q projector opts after ApplySyncOpts(true,true,true) = %+v; want all true", id, got)
		}
	}
}

// TestPoolApplySyncOptsIsNoopForEmptyPool: an operator who has not
// logged in (so no SyncSession is attached) must see the toggle take
// effect on disk and the pool do nothing — no panic, no error return.
func TestPoolApplySyncOptsIsNoopForEmptyPool(t *testing.T) {
	res := &config.Resolved{ConfigPath: t.TempDir() + "/mivia.toml"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	pool := NewSessionPool(nil, res, state, false)

	// Should not panic.
	pool.ApplySyncOpts(false, false, false)
}

// TestPoolApplySyncOptsSurvivesOneStoppedSession: when at least one
// attached session is stopped (remoteEnded), ApplySyncOpts must keep
// fan-outing to the others. A stopped session is the no-op return
// path on SetSyncOpts; it must not abort the loop.
func TestPoolApplySyncOptsSurvivesOneStoppedSession(t *testing.T) {
	res := &config.Resolved{ConfigPath: t.TempDir() + "/mivia.toml"}
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry()}
	pool := NewSessionPool(nil, res, state, false)

	live := chatsync.NewSyncSessionForTest(chatsync.ProjectorOptions{})
	stopped := chatsync.NewSyncSessionForTest(chatsync.ProjectorOptions{})
	stopped.StoppedForTest()

	pool.syncSessions["live"] = live
	pool.syncSessions["stopped"] = stopped

	pool.ApplySyncOpts(true, false, true)

	if !live.ProjectorOptsForTest().IncludeThinking {
		t.Errorf("live session IncludeThinking not flipped: %+v", live.ProjectorOptsForTest())
	}
}
