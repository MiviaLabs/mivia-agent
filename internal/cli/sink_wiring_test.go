package cli

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// recordingBus collects every published event. It is safe for concurrent use
// because the bus delivers each kind on its own subscriber goroutine.
type recordingBus struct {
	mu sync.Mutex
	ev []events.Event
}

func newRecordingBus() *recordingBus { return &recordingBus{} }

func (r *recordingBus) HandleEvent(_ context.Context, ev events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ev = append(r.ev, ev)
}

func (r *recordingBus) events() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.Event(nil), r.ev...)
}

var _ events.Handler = (*recordingBus)(nil)

// invocationKinds is the event set the session sink publishes.
func invocationKinds() []events.Kind {
	return []events.Kind{
		events.KindInvocationStarted,
		events.KindInvocationCompleted,
		events.KindInvocationRetrying,
	}
}

// byKind groups events by kind. The bus delivers each kind on its own
// goroutine, so cross-kind arrival order is not deterministic.
func byKind(evs []events.Event) map[events.Kind][]events.Event {
	out := map[events.Kind][]events.Event{}
	for _, ev := range evs {
		out[ev.Kind] = append(out[ev.Kind], ev)
	}
	return out
}

// assertInvocationEvent checks the fields every sink-published event must
// carry for one dispatched invocation.
func assertInvocationEvent(t *testing.T, ev events.Event, id, name, detail string) {
	t.Helper()
	if ev.Name != name {
		t.Fatalf("name = %q, want %q", ev.Name, name)
	}
	if ev.Detail != detail {
		t.Fatalf("detail = %q, want %q", ev.Detail, detail)
	}
	if ev.Metadata["id"] != id {
		t.Fatalf("metadata id = %q, want %q", ev.Metadata["id"], id)
	}
	if ev.AgentTask != id {
		t.Fatalf("AgentTask = %q, want %q", ev.AgentTask, id)
	}
	if ev.AgentName != name || ev.AgentDepth != 1 {
		t.Fatalf("attribution = %q/%d, want %s/1", ev.AgentName, ev.AgentDepth, name)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("timestamp is zero")
	}
}

// TestSessionDispatcherSinkPublishesLifecycle wires a sink through
// NewSessionDispatcher and asserts one invocation publishes started and
// completed lifecycle events carrying the invocation id.
func TestSessionDispatcherSinkPublishesLifecycle(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m"}, nil)
	bus := events.New()
	sess.EventBus = bus
	defer bus.Close()
	rec := newRecordingBus()
	bus.SubscribeMany(invocationKinds(), rec)

	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:  tools.NewRegistry(),
		Completer: nullCompleter{},
		Model:     "test-model",
		Config:    config.DefaultSubagentConfig,
		Sink:      sessionInvocationSink(sess),
	})
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	defer d.Close()
	if err := d.Register(runtime.Subagent, "sink-tool", handlerFunc(func(context.Context, runtime.Request) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})); err != nil {
		t.Fatalf("register sink-tool: %v", err)
	}

	result := d.Invoke(context.Background(), runtime.Request{
		ID: "sink-inv-1", Kind: runtime.Subagent, Name: "sink-tool",
		Input: json.RawMessage(`"do it"`),
	})
	if result.Err != nil {
		t.Fatalf("invoke: %v", result.Err)
	}
	bus.Flush()

	grouped := byKind(rec.events())
	started := grouped[events.KindInvocationStarted]
	completed := grouped[events.KindInvocationCompleted]
	if len(started) != 1 || len(completed) != 1 {
		t.Fatalf("published kinds = %v, want one started and one completed", rec.events())
	}
	assertInvocationEvent(t, started[0], "sink-inv-1", "sink-tool", "subagent started")
	assertInvocationEvent(t, completed[0], "sink-inv-1", "sink-tool", "subagent completed")
}

// TestNewSessionDispatcherSinkNilByDefault pins that omitting Sink leaves the
// policy sink nil, so every existing caller is unchanged.
func TestNewSessionDispatcherSinkNilByDefault(t *testing.T) {
	d, err := NewSessionDispatcher(SessionDispatcherOpts{
		Registry:  tools.NewRegistry(),
		Completer: nullCompleter{},
		Model:     "test-model",
		Config:    config.DefaultSubagentConfig,
	})
	if err != nil {
		t.Fatalf("NewSessionDispatcher: %v", err)
	}
	defer d.Close()
	if sink := d.Policy().Sink; sink != nil {
		t.Fatalf("Policy().Sink = %p, want nil", sink)
	}
}

// TestSessionInvocationSinkMapsAndPublishes drives the sink closure directly
// and asserts each runtime lifecycle event maps onto the bus event exactly.
func TestSessionInvocationSinkMapsAndPublishes(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "m"}, nil)
	bus := events.New()
	sess.EventBus = bus
	defer bus.Close()
	rec := newRecordingBus()
	bus.SubscribeMany(invocationKinds(), rec)

	sink := sessionInvocationSink(sess)
	sink(runtime.Event{Type: "started", Metadata: runtime.Metadata{ID: "a", ParentID: "p", TurnID: "t", Name: "n", Kind: "tool", Status: "started"}})
	sink(runtime.Event{Type: "retrying", Metadata: runtime.Metadata{ID: "a", ParentID: "p", TurnID: "t", Name: "n", Kind: "tool", Status: "retrying"}})
	sink(runtime.Event{Type: "completed", Metadata: runtime.Metadata{ID: "a", ParentID: "p", TurnID: "t", Name: "n", Kind: "tool", Status: "completed"}})
	bus.Flush()

	grouped := byKind(rec.events())
	if len(grouped[events.KindInvocationStarted]) != 1 ||
		len(grouped[events.KindInvocationRetrying]) != 1 ||
		len(grouped[events.KindInvocationCompleted]) != 1 {
		t.Fatalf("published kinds = %v, want one each of started/retrying/completed", rec.events())
	}
	assertInvocationEvent(t, grouped[events.KindInvocationStarted][0], "a", "n", "tool started")
	assertInvocationEvent(t, grouped[events.KindInvocationRetrying][0], "a", "n", "tool retrying")
	completed := grouped[events.KindInvocationCompleted][0]
	assertInvocationEvent(t, completed, "a", "n", "tool completed")
	if completed.Metadata["turn"] != "t" || completed.Metadata["parent"] != "p" {
		t.Fatalf("completed metadata = %v, want turn=t parent=p", completed.Metadata)
	}
}

// TestSessionInvocationSinkNilSafe pins that a nil session bus and a nil
// session both make the sink a silent no-op.
func TestSessionInvocationSinkNilSafe(t *testing.T) {
	noBus := chat.NewSession(&config.Resolved{Model: "m"}, nil)
	sessionInvocationSink(noBus)(runtime.Event{Type: "started", Metadata: runtime.Metadata{ID: "x"}})
	sessionInvocationSink(nil)(runtime.Event{Type: "completed", Metadata: runtime.Metadata{ID: "x"}})
}
