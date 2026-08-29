package conversation

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

func TestDbgOverlayResync(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	h := &recordingHandle{id: "t1"}
	s.active = h
	s.queue = []string{"first", "second"}
	s.queueOverlay.Open(s.queue)
	got := s.forceSendHead()
	if got.pendingForce == nil || *got.pendingForce != "first" {
		t.Fatal("force-send with overlay open failed")
	}
	if items := got.queueOverlay.Items(); len(items) != 1 || items[0] != "second" {
		t.Fatalf("overlay must resync to [second], got %v", items)
	}
}
