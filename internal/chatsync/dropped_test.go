package chatsync

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func TestProjectorEmitsSyncDroppedOnDropCounterAdvance(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// 1. Initial event with 0 drops
	ev1 := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "prompt",
		Timestamp: time.Now(),
	}
	we1 := p.ProjectWithDrops(ev1, 0)
	if len(we1) != 1 {
		t.Fatalf("first event produced %d events, want 1", len(we1))
	}
	if we1[0].Seq != 1 || we1[0].Type != TypeTurnStarted {
		t.Errorf("we1 = %+v, want seq 1 TypeTurnStarted", we1[0])
	}

	// 2. Second event with drop counter jump to 5
	ev2 := events.Event{
		Kind:      events.KindAssistant,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Content:   "answer",
		Timestamp: time.Now(),
	}
	we2 := p.ProjectWithDrops(ev2, 5)
	if len(we2) != 2 {
		t.Fatalf("event with drops produced %d wire events, want 2", len(we2))
	}

	// First emitted event must be sync.dropped immediately preceding the regular event
	dropEv := we2[0]
	if dropEv.Seq != 2 || dropEv.Type != TypeSyncDropped {
		t.Fatalf("dropEv = %+v, want seq 2 TypeSyncDropped", dropEv)
	}
	dropPayload, ok := dropEv.Payload.(*SyncDroppedPayload)
	if !ok {
		t.Fatalf("dropPayload type = %T, want *SyncDroppedPayload", dropEv.Payload)
	}
	if dropPayload.Dropped != 5 || dropPayload.TotalDropped != 5 {
		t.Errorf("dropPayload = %+v, want Dropped=5, TotalDropped=5", dropPayload)
	}
	if dropPayload.Turn != "turn:1" {
		t.Errorf("dropPayload turn = %q, want turn:1", dropPayload.Turn)
	}

	// Second emitted event is the regular assistant message
	assistEv := we2[1]
	if assistEv.Seq != 3 || assistEv.Type != TypeAssistantMessage {
		t.Fatalf("assistEv = %+v, want seq 3 TypeAssistantMessage", assistEv)
	}
}

func TestProjectorFlushEmitsSyncDropped(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// Flush with 0 drops -> 0 events
	we0 := p.Flush(0)
	if len(we0) != 0 {
		t.Fatalf("flush with 0 drops produced %d events, want 0", len(we0))
	}

	// Flush with 3 drops -> 1 sync.dropped event
	we1 := p.Flush(3)
	if len(we1) != 1 {
		t.Fatalf("flush with 3 drops produced %d events, want 1", len(we1))
	}
	if we1[0].Seq != 1 || we1[0].Type != TypeSyncDropped {
		t.Fatalf("we1 = %+v, want seq 1 TypeSyncDropped", we1[0])
	}
	p1 := we1[0].Payload.(*SyncDroppedPayload)
	if p1.Dropped != 3 || p1.TotalDropped != 3 {
		t.Errorf("p1 = %+v, want Dropped=3, TotalDropped=3", p1)
	}

	// Subsequent flush with same total drops -> 0 events
	we2 := p.Flush(3)
	if len(we2) != 0 {
		t.Fatalf("second flush produced %d events, want 0", len(we2))
	}
}

func TestProjectorDecreasingDropCounterEmitsNothing(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// Advance drops to 10
	_ = p.Flush(10)
	if p.LastSeq() != 1 {
		t.Fatalf("lastSeq = %d, want 1", p.LastSeq())
	}

	// Drop counter decreases to 5 (e.g. counter wrap or invalid read)
	ev := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "test",
		Timestamp: time.Now(),
	}
	we := p.ProjectWithDrops(ev, 5)
	if len(we) != 1 {
		t.Fatalf("project produced %d events, want 1", len(we))
	}
	if we[0].Type == TypeSyncDropped {
		t.Errorf("decreasing drops emitted sync.dropped")
	}
	if we[0].Seq != 2 {
		t.Errorf("seq = %d, want 2", we[0].Seq)
	}
}
