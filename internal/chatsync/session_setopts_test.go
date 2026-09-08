package chatsync

import "testing"

// TestSyncSessionSetSyncOptsFlipsAttachedProjector pins the per-session
// re-arm seam: a SyncSession that owns an attached projector must, on
// SetSyncOpts, flip the projector's gates so the next projection round
// uses the new values.
func TestSyncSessionSetSyncOptsFlipsAttachedProjector(t *testing.T) {
	ss := NewSyncSessionForTest(ProjectorOptions{
		IncludeThinking: false,
		IncludeToolIO:   false,
		StreamAssistant: false,
	})

	ss.SetSyncOpts(true, true, true)

	got := ss.ProjectorOptsForTest()
	if !got.IncludeThinking || !got.IncludeToolIO || !got.StreamAssistant {
		t.Errorf("projector opts after SetSyncOpts(true,true,true) = %+v; want all three true", got)
	}
}

// TestSyncSessionSetSyncOptsNoopBeforeAttach: when the projector is
// nil (the session is created but the worker has not yet attached),
// SetSyncOpts must not panic and must record the desired values so the
// next attach picks them up. The test exercises the storage path on
// ss.opts; the attach path consumes it when it builds the projector.
func TestSyncSessionSetSyncOptsNoopBeforeAttach(t *testing.T) {
	// Build a SyncSession whose projector is intentionally not assigned
	// (mimics pre-attach state). We can't go through OpenSession (it
	// requires a real client), so we hand-construct and immediately
	// call the setter before any other operation.
	ss := &SyncSession{
		opts: SessionOptions{ProjectorOptions: ProjectorOptions{
			IncludeThinking: false,
			IncludeToolIO:   false,
			StreamAssistant: false,
		}},
	}

	ss.SetSyncOpts(true, false, true) // must not panic on a nil projector

	got := ss.ProjectorOptsForTest()
	if !got.IncludeThinking || got.IncludeToolIO || !got.StreamAssistant {
		t.Errorf("opts after SetSyncOpts = %+v; want (true, false, true)", got)
	}
}

// TestSyncSessionSetSyncOptsIsNoopOnStopped: a session that has
// stopped (remoteEnded true) must accept SetSyncOpts without
// panicking, and the projector's LIVE opts must stay at their prior
// value - the toggle cannot reach a session that is going down. The
// settings screen can race with a session tearing down; the live
// re-arm path must fail closed rather than mutate a projector nobody
// reads from anymore.
func TestSyncSessionSetSyncOptsIsNoopOnStopped(t *testing.T) {
	ss := NewSyncSessionForTest(ProjectorOptions{StreamAssistant: true})
	ss.StoppedForTest()

	ss.SetSyncOpts(false, false, false) // must not panic

	if got := ss.ProjectorOptsForTest(); !got.StreamAssistant {
		t.Errorf("projector opts after SetSyncOpts on a stopped session = %+v; want StreamAssistant still true (the toggle must not reach a stopped session's projector)", got)
	}
}
