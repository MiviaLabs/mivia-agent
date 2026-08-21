package legacytui

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
)

// publishSurfaceLoop stands in for a turn boundary admitting a deferred tool:
// TryPublishAgentSurface writes Session.Tools under the session lock. Every
// reader of that field must go through AgentSurfaceSnapshot, which is the
// documented contract for the field and the point of these tests.
//
// Package-local copy of internal/cli's helper of the same name
// (surface_snapshot_race_test.go): cli's staying classic/model-binding races
// need their own copy.
func publishSurfaceLoop(t *testing.T, sess *chat.Session, iterations int) func() {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			sess.TryPublishAgentSurface(chat.AgentSurfacePublication{
				Prompt:   "root",
				MaxSteps: 4,
				Registry: tierRegistry("read_file", "grep"),
			})
		}
	}()
	return func() { <-done }
}

// TestTuiToolsSlashSnapshotsTheSurface: /tools runs on the Bubble Tea update
// goroutine with no m.waiting gate, so it reads the tool surface while a turn
// boundary is publishing a widened one. Run under -race.
func TestTuiToolsSlashSnapshotsTheSurface(t *testing.T) {
	m := newSmokeModel(t)
	m.session.Tools = tierRegistry("read_file")
	wait := publishSurfaceLoop(t, m.session, 300)
	for i := 0; i < 300; i++ {
		if !m.handleTuiInfoSlash("/tools", []string{"/tools"}) {
			t.Fatal("/tools was not handled")
		}
	}
	wait()
	if m.overlay == nil {
		t.Fatal("/tools opened no overlay")
	}
}
