package chatsync

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestAHookRunReachesTheSyncSession pins the DELIVERY hop, not the projector.
//
// The projector has had a hook arm, a wire type, a contract row and a metrics
// entry since hook runs shipped - but syncKinds, the allowlist of the one bus
// subscription that feeds the projector in production, never named the kind.
// Every projector-level test passed because it called Project directly, and
// not one hook.ran row ever reached a viewer. A kind the wire advertises but
// the subscription drops is invisible to every test below the session.
func TestAHookRunReachesTheSyncSession(t *testing.T) {
	rec, srv := newRecordingServer(t, "sess-hook-feed")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-hook-feed", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Hook Feed",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = syncSess.Stop(context.Background()) })

	ev := hookEvent("PreToolUse", "guard.py", "run_command", "c1", "policy: no network", true)
	ev.SessionID = "sess-hook-feed"
	ev.TurnID = "turn:1"
	bus.Publish(ev)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		stored := append([]EventItem(nil), rec.events...)
		rec.mu.Unlock()
		for _, e := range stored {
			if e.Type == TypeHookRan {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s event reached the recording server; the sync subscription is still dropping hook runs", TypeHookRan)
}
