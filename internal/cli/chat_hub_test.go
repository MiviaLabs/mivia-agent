package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/hub"
)

func newHubTestSession(t *testing.T, sessionID string) *chat.Session {
	t.Helper()
	sess := chat.NewSession(&config.Resolved{}, nil)
	sess.SessionID = sessionID
	sess.EventBus = events.New()
	return sess
}

// Regression: internal/hub's workspace is shared by every session using the
// default (project-less) workspace - e.g. mivia-agent-desktop's own sibling
// threads. Before externalEventBelongsToSession existed, chatHubSink
// rendered ANY relayed event regardless of which session it belonged to,
// so one thread's own conversation appeared as a phantom "external turn"
// in every other open thread sharing that workspace.
func TestExternalEventBelongsToSession(t *testing.T) {
	sess := newHubTestSession(t, "sess-mine")

	cases := []struct {
		name string
		ev   events.Event
		want bool
	}{
		{"matching session id", events.Event{SessionID: "sess-mine"}, true},
		{"different session id (sibling thread)", events.Event{SessionID: "sess-other"}, false},
		{"empty session id", events.Event{SessionID: ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := externalEventBelongsToSession(sess, c.ev); got != c.want {
				t.Fatalf("externalEventBelongsToSession(%+v) = %v, want %v", c.ev, got, c.want)
			}
		})
	}
}

// TestRenderExternalEventTracksConcurrentRunsIndependently: two different
// run_ids relayed for the SAME session (e.g. two other participants both
// resumed this exact session) must not corrupt each other's turn-start
// framing or content - each gets its own "external_turn_start" and its own
// chunks, keyed by run_id, not a single shared scalar.
func TestRenderExternalEventTracksConcurrentRunsIndependently(t *testing.T) {
	var buf bytes.Buffer
	state := &externalTurnState{seenRunIDs: make(map[string]struct{})}

	renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnStart, SessionID: "s1", Detail: "hi from run A"})
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindAssistant, SessionID: "s1", TurnID: "turn:1", Content: "reply A"})
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnStart, SessionID: "s1", Detail: "hi from run B"})
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindAssistant, SessionID: "s1", TurnID: "turn:7", Content: "reply B"})
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnEnd, SessionID: "s1", TurnID: "turn:1"})
	renderExternalEvent(&buf, state, events.Event{Kind: events.KindTurnEnd, SessionID: "s1", TurnID: "turn:7"})

	lines := decodeNDJSONLines(t, buf.String())
	byType := map[string][]ndjsonEvent{}
	for _, line := range lines {
		byType[line.Type] = append(byType[line.Type], line)
	}

	if len(byType["external_turn_start"]) != 2 {
		t.Fatalf("expected 2 external_turn_start lines, got %d: %+v", len(byType["external_turn_start"]), byType["external_turn_start"])
	}
	starts := map[string]string{}
	for _, s := range byType["external_turn_start"] {
		starts[s.RunID] = s.Text
	}
	if starts["turn:1"] != "hi from run A" {
		t.Fatalf("turn:1 start text = %q, want %q", starts["turn:1"], "hi from run A")
	}
	if starts["turn:7"] != "hi from run B" {
		t.Fatalf("turn:7 start text = %q, want %q", starts["turn:7"], "hi from run B")
	}

	chunks := map[string]string{}
	for _, c := range byType["external_chunk"] {
		chunks[c.RunID] = c.Text
	}
	if chunks["turn:1"] != "reply A" || chunks["turn:7"] != "reply B" {
		t.Fatalf("chunks not attributed independently: %+v", chunks)
	}

	if len(byType["external_done"]) != 2 {
		t.Fatalf("expected 2 external_done lines, got %d", len(byType["external_done"]))
	}
	if len(state.seenRunIDs) != 0 {
		t.Fatalf("expected seenRunIDs cleared after both runs ended, got %+v", state.seenRunIDs)
	}
}

func decodeNDJSONLines(t *testing.T, s string) []ndjsonEvent {
	t.Helper()
	var out []ndjsonEvent
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var ev ndjsonEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode ndjson line %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

// bufSink relays events through the exact production filter/render path
// (externalEventBelongsToSession + renderExternalEvent), writing to a
// bytes.Buffer instead of chatHubSink's hardcoded os.Stdout - the only
// difference from the real line-mode sink, so this exercises real
// production logic, not a reimplementation of it.
func newBufSink(sess *chat.Session) (hub.Sink, *hubOutBuffer) {
	buf := &hubOutBuffer{}
	state := &externalTurnState{seenRunIDs: make(map[string]struct{})}
	return func(ev events.Event) {
		if !externalEventBelongsToSession(sess, ev) {
			return
		}
		renderExternalEvent(buf, state, ev)
	}, buf
}

type hubOutBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *hubOutBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *hubOutBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitOutFixedWindow blocks for exactly window, via a ticker/timer select
// rather than time.Sleep, for a negative assertion (no positive signal to
// poll for) that still wants to give async delivery a real chance.
func waitOutFixedWindow(window time.Duration) {
	deadline := time.After(window)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-deadline:
			return
		}
	}
}

func waitForNonEmpty(t *testing.T, timeout time.Duration, publish func(), buf *hubOutBuffer) {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		publish()
		if buf.String() != "" {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("no output within %s", timeout)
		}
	}
}

// TestHubDoesNotBleedBetweenSiblingSessions is the end-to-end regression
// test for the reported bug: mivia-agent-desktop's own sibling threads
// (each its own *chat.Session, no project picked) default to ONE shared
// workspace and so join the SAME hub. Over a real hub.Join/socket pair,
// two DIFFERENT sessions must not see each other's turns; two participants
// on the SAME session (the actual intended use case - an external terminal
// TUI and a desktop thread resuming one conversation) must.
func TestHubDoesNotBleedBetweenSiblingSessions(t *testing.T) {
	dir := t.TempDir()

	threadA := newHubTestSession(t, "session-A")
	threadB := newHubTestSession(t, "session-B") // a sibling thread, different conversation
	sinkA, outA := newBufSink(threadA)
	sinkB, outB := newBufSink(threadB)

	handleA := hub.Join(dir, threadA, sinkA)
	defer handleA.Leave()
	handleB := hub.Join(dir, threadB, sinkB)
	defer handleB.Leave()

	// Prime the hub connections (see internal/hub's own tests for why a
	// retry-publish loop, not a fixed sleep, is used to detect "connected")
	// before trusting the negative assertion below - a THIRD participant
	// that resumes the SAME session as thread A gives a positive signal to
	// poll for, proving the mesh (A, B, and this warmup participant) is
	// actually connected and delivering, unlike thread B's sink which is
	// expected to stay silent for exactly the property under test.
	warmupSameSession := newHubTestSession(t, "session-A")
	sinkWarmup, outWarmup := newBufSink(warmupSameSession)
	handleWarmup := hub.Join(dir, warmupSameSession, sinkWarmup)
	defer handleWarmup.Leave()
	waitForNonEmpty(t, 5*time.Second, func() {
		threadA.EventBus.Publish(events.Event{Kind: events.KindTurnStart, SessionID: "session-A", Detail: "warmup"})
		threadA.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "session-A", TurnID: "warmup-turn", Content: "warmup"})
	}, outWarmup)
	if outA.String() != "" {
		t.Fatalf("thread A must never render its own relayed-back turn, got %q", outA.String())
	}
	if outB.String() != "" {
		t.Fatalf("sibling session B rendered warmup traffic meant for session A: %q", outB.String())
	}

	// Thread A runs a real turn. Thread B (a sibling, unrelated session)
	// must render NOTHING for it.
	threadA.EventBus.Publish(events.Event{Kind: events.KindTurnStart, SessionID: "session-A", Detail: "hello from thread A's user"})
	threadA.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "session-A", TurnID: "turn:1", Content: "thread A's reply"})
	threadA.EventBus.Publish(events.Event{Kind: events.KindTurnEnd, SessionID: "session-A", TurnID: "turn:1"})

	waitOutFixedWindow(300 * time.Millisecond)
	if outB.String() != "" {
		t.Fatalf("sibling session B must not receive thread A's turns, got %q", outB.String())
	}

	// Now a SECOND participant resumes the SAME session as thread A (the
	// actual feature: an external terminal TUI, or another desktop window,
	// on the identical conversation) - it MUST see the relay.
	sameSession := newHubTestSession(t, "session-A")
	sinkSame, outSame := newBufSink(sameSession)
	handleSame := hub.Join(dir, sameSession, sinkSame)
	defer handleSame.Leave()

	waitForNonEmpty(t, 5*time.Second, func() {
		threadA.EventBus.Publish(events.Event{Kind: events.KindTurnStart, SessionID: "session-A", Detail: "second turn"})
		threadA.EventBus.Publish(events.Event{Kind: events.KindAssistant, SessionID: "session-A", TurnID: "turn:2", Content: "second reply"})
	}, outSame)

	lines := decodeNDJSONLines(t, outSame.String())
	found := false
	for _, l := range lines {
		if l.Type == "external_chunk" && l.Text == "second reply" {
			found = true
		}
	}
	if !found {
		t.Fatalf("same-session participant did not receive the relayed reply: %q", outSame.String())
	}
}
