package chat

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// deltaCollector subscribes to KindAssistant and records every event, so a
// test can assert on live delta publication order and content.
type deltaCollector struct {
	mu  sync.Mutex
	evs []events.Event
}

func newDeltaCollector(bus *events.Bus) *deltaCollector {
	c := &deltaCollector{}
	bus.Subscribe(events.KindAssistant, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.evs = append(c.evs, ev)
	}))
	return c
}

func (c *deltaCollector) snapshot() []events.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]events.Event, len(c.evs))
	copy(out, c.evs)
	return out
}

// TestPlainChatStreamsLiveDeltasToEventBus is the --no-tools counterpart of
// internal/agent/loop_test.go's teeWriter delta tests: a plain (non-tool)
// chat turn never reached internal/agent at all, so it never published to
// EventBus - a cross-process observer (internal/hub's relay) saw nothing
// for the entire generation window of a --no-tools session, not even the
// "thinking" indicator the tool-enabled gap (fixed separately) still showed.
func TestPlainChatStreamsLiveDeltasToEventBus(t *testing.T) {
	store, _ := openSharedContextStore(t)
	completer := &capturingStreamCompleter{}
	session, _ := newPlainContextSession(t, store, completer, nil)
	session.EventBus = events.New()
	defer session.EventBus.Close()
	collector := newDeltaCollector(session.EventBus)
	session.EventBus.Flush()

	if _, err := session.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	session.EventBus.Flush()

	evs := collector.snapshot()
	if len(evs) == 0 {
		t.Fatal("expected at least one live KindAssistant delta, got none")
	}
	var content strings.Builder
	for _, ev := range evs {
		if ev.Detail != "delta" {
			t.Fatalf("expected every plain-chat EventAssistant to carry Detail=delta, got %+v", ev)
		}
		if ev.SessionID != session.SessionID {
			t.Fatalf("delta SessionID = %q, want %q", ev.SessionID, session.SessionID)
		}
		content.WriteString(ev.Content)
	}
	if content.String() != "answer" {
		t.Fatalf("reassembled delta content = %q, want %q", content.String(), "answer")
	}
}

// TestPlainChatWithNoEventBusStreamsNormally: a session with no hub
// membership (EventBus nil - the common case, most sessions never join a
// hub) must stream exactly as before this change; eventPublishingWriter
// must never be a hard dependency on a bus existing.
func TestPlainChatWithNoEventBusStreamsNormally(t *testing.T) {
	store, _ := openSharedContextStore(t)
	completer := &capturingStreamCompleter{}
	session, _ := newPlainContextSession(t, store, completer, nil)

	var out strings.Builder
	reply, err := session.SendUser(context.Background(), "hello", &out)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "answer" || out.String() != "answer" {
		t.Fatalf("reply=%q out=%q, want both %q", reply, out.String(), "answer")
	}
}
