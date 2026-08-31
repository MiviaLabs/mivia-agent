package clichat

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/hub"
)

// TestJoinHubReusesAnExistingBus pins the half of the fix that keeps the bus
// stable across a join.
//
// JoinHub used to be the only thing that created a bus, which is why the TUI -
// which never joins - had none. Now every surface gets one at session
// construction, so JoinHub must adopt it rather than replace it: replacing it
// would orphan every subscriber registered before the join, silently.
func TestJoinHubReusesAnExistingBus(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m"}, nil)
	existing := events.New()
	t.Cleanup(existing.Close)
	sess.EventBus = existing

	// A session with no SQLite-backed context store returns a no-op handle,
	// which is enough to exercise the bus branch.
	_ = JoinHub(sess, hub.Sink(nil))

	if sess.EventBus != existing {
		t.Error("JoinHub replaced the session's bus; subscribers registered before the join are orphaned")
	}
}

// TestJoinHubStillMintsABusWhenAbsent keeps the fallback, so a session built
// outside runConfiguredChatOnce is not left publishing into nil.
func TestJoinHubStillMintsABusWhenAbsent(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m"}, nil)
	if sess.EventBus != nil {
		t.Fatal("a fresh chat.NewSession should carry no bus")
	}

	_ = JoinHub(sess, hub.Sink(nil))

	if sess.EventBus == nil {
		t.Error("JoinHub left the session with no bus")
	}
}
