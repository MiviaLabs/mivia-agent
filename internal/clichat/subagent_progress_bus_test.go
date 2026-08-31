package clichat

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// busSubagentCompleter is a stand-in provider; these tests never run a turn,
// they drive emitSubagentProgress directly.
type busSubagentCompleter struct{}

func (busSubagentCompleter) Name() string { return "fake" }
func (busSubagentCompleter) Chat(context.Context, provider.Request) (string, error) {
	return "", nil
}
func (busSubagentCompleter) ChatStream(context.Context, provider.Request, interface{ Write([]byte) (int, error) }) (string, error) {
	return "", nil
}

func subagentBusFixture(t *testing.T) (*chat.Session, chan events.Event) {
	t.Helper()
	sess := chat.NewSession(&config.Resolved{Model: "m"}, nil)
	bus := events.New()
	t.Cleanup(bus.Close)
	sess.EventBus = bus

	got := make(chan events.Event, 8)
	bus.SubscribeMany(
		[]events.Kind{
			events.KindSubagentStart, events.KindSubagentEnd,
			events.KindSubagentHeartbeat, events.KindSubagentDone,
		},
		events.HandlerFunc(func(_ context.Context, ev events.Event) { got <- ev }),
	)
	return sess, got
}

func awaitEvent(t *testing.T, got chan events.Event) (events.Event, bool) {
	t.Helper()
	select {
	case ev := <-got:
		return ev, true
	case <-time.After(2 * time.Second):
		return events.Event{}, false
	}
}

// TestSubagentProgressReachesTheSessionBus is the regression. The publish used
// to target a package-level bus that nothing in production ever set, so all
// four subagent kinds - which internal/hub lists as relayable - reached no
// consumer anywhere, while the code read as though a second surface was fed.
func TestSubagentProgressReachesTheSessionBus(t *testing.T) {
	sess, got := subagentBusFixture(t)
	unbind := SetSubagentSession(sess)
	defer unbind()

	emitSubagentProgress(agent.Event{
		Kind:       agent.EventSubagentStart,
		Name:       "reviewer",
		ToolCallID: "call-1",
	})

	ev, ok := awaitEvent(t, got)
	if !ok {
		t.Fatal("no subagent event reached the bus")
	}
	if ev.Kind != events.KindSubagentStart {
		t.Errorf("kind = %q, want subagent_start", ev.Kind)
	}
	if ev.Name != "reviewer" {
		t.Errorf("Name = %q, want reviewer", ev.Name)
	}
}

// TestSubagentEventCarriesSessionAttribution covers the half that makes the
// publish observable. A hub receiver rejects an event with no SessionID rather
// than matching two empty strings, so a published-but-unattributed event is
// dropped on arrival and is indistinguishable from never having been sent.
func TestSubagentEventCarriesSessionAttribution(t *testing.T) {
	sess, got := subagentBusFixture(t)
	defer SetSubagentSession(sess)()

	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentHeartbeat, Name: "reviewer"})

	ev, ok := awaitEvent(t, got)
	if !ok {
		t.Fatal("no subagent event reached the bus")
	}
	if ev.SessionID == "" {
		t.Error("subagent event carried no SessionID; a hub receiver drops it")
	}
	if ev.SessionID != sess.SessionID {
		t.Errorf("SessionID = %q, want %q", ev.SessionID, sess.SessionID)
	}
}

// TestUnboundSessionPublishesNothing keeps the binding scoped to a live chat
// surface. A leaked binding would publish a later session's subagent activity
// into a stale session's bus.
func TestUnboundSessionPublishesNothing(t *testing.T) {
	sess, got := subagentBusFixture(t)
	unbind := SetSubagentSession(sess)
	unbind()

	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentStart, Name: "reviewer"})

	select {
	case ev := <-got:
		t.Fatalf("an unbound session still received %+v", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestSubagentProgressStillReachesTheLocalSink guards the path that already
// worked. The bus publish is additive; the registered UI callback must keep
// receiving every event exactly once.
func TestSubagentProgressStillReachesTheLocalSink(t *testing.T) {
	sess, _ := subagentBusFixture(t)
	defer SetSubagentSession(sess)()

	var seen []agent.Event
	token := SetSubagentProgress(func(e agent.Event) { seen = append(seen, e) })
	defer ClearSubagentProgress(token)

	emitSubagentProgress(agent.Event{Kind: agent.EventSubagentDone, Name: "reviewer"})

	if len(seen) != 1 {
		t.Fatalf("local sink saw %d events, want exactly 1", len(seen))
	}
	if seen[0].Name != "reviewer" {
		t.Errorf("local sink got Name %q, want reviewer", seen[0].Name)
	}
}
