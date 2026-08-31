package chat

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// reasoningStreamCompleter is a completer that honours the documented
// StreamWriter contract: "StreamWriter receives content deltas when ChatTurn
// streams (Stream=true)" (internal/provider/provider.go). It streams its reply
// and reports the reasoning that produced it, which is what a real provider
// does - internal/provider/openai_compat_stream.go's readTurnStream returns
// content and reasoning together, and AnthropicCompleter.ChatStream is
// literally ChatTurn with a StreamWriter and the reasoning discarded.
type reasoningStreamCompleter struct {
	mu        sync.Mutex
	reply     string
	reasoning string
	sawStream bool
}

func (c *reasoningStreamCompleter) Name() string { return "reasoning-fake" }

func (c *reasoningStreamCompleter) Chat(ctx context.Context, req provider.Request) (string, error) {
	return c.reply, nil
}

func (c *reasoningStreamCompleter) ChatStream(ctx context.Context, req provider.Request, w io.Writer) (string, error) {
	req.Stream = true
	req.StreamWriter = w
	resp, err := c.ChatTurn(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *reasoningStreamCompleter) ChatTurn(_ context.Context, req provider.Request) (*provider.Response, error) {
	if req.StreamWriter != nil {
		c.mu.Lock()
		c.sawStream = true
		c.mu.Unlock()
		_, _ = io.WriteString(req.StreamWriter, c.reply)
	}
	return &provider.Response{
		Content:          c.reply,
		ReasoningContent: c.reasoning,
		FinishReason:     "stop",
	}, nil
}

func (c *reasoningStreamCompleter) streamed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sawStream
}

// collectKind records every event of one kind, so a test can assert on what a
// turn published without depending on cross-kind arrival order (events.Bus
// registers one subscription per kind, so that order is not meaningful).
func collectKind(bus *events.Bus, kind events.Kind) func() []events.Event {
	var (
		mu  sync.Mutex
		evs []events.Event
	)
	bus.Subscribe(kind, events.HandlerFunc(func(_ context.Context, ev events.Event) {
		mu.Lock()
		defer mu.Unlock()
		evs = append(evs, ev)
	}))
	return func() []events.Event {
		mu.Lock()
		defer mu.Unlock()
		out := make([]events.Event, len(evs))
		copy(out, evs)
		return out
	}
}

// TestPlainTurnPublishesReasoning is the gap: a --no-tools turn produced no
// KindThinking, on any surface, for any consumer.
//
// The cause was not a missing publisher. It was that the value never existed
// on that path: Completer.ChatStream returns (string, error) and drops the
// reasoning the provider already decoded, so there was nothing to publish.
// internal/agent's loop gets it from ChatTurn's *Response and emits
// EventThinking (loop_step.go); the plain path never reaches internal/agent.
func TestPlainTurnPublishesReasoning(t *testing.T) {
	store, _ := openSharedContextStore(t)
	completer := &reasoningStreamCompleter{reply: "answer", reasoning: "let me think"}
	session, _ := newPlainContextSession(t, store, completer, nil)
	session.EventBus = events.New()
	defer session.EventBus.Close()
	thinking := collectKind(session.EventBus, events.KindThinking)
	session.EventBus.Flush()

	if _, err := session.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	session.EventBus.Flush()

	evs := thinking()
	if len(evs) == 0 {
		t.Fatal("a plain turn whose provider reported reasoning published no KindThinking")
	}
	if evs[0].Content != "let me think" {
		t.Errorf("thinking Content = %q, want the provider's reasoning", evs[0].Content)
	}
	if evs[0].SessionID != session.SessionID {
		t.Errorf("thinking SessionID = %q, want %q", evs[0].SessionID, session.SessionID)
	}
	if evs[0].TurnID == "" {
		t.Error("thinking carried no TurnID; a consumer cannot attribute it to a turn")
	}
}

// TestPlainTurnStillStreamsWhileReporting guards the half most likely to
// regress. Reading the reasoning means taking the turn through ChatTurn with a
// StreamWriter instead of ChatStream, so the reply must still arrive
// incrementally - not in one lump at the end.
func TestPlainTurnStillStreamsWhileReporting(t *testing.T) {
	store, _ := openSharedContextStore(t)
	completer := &reasoningStreamCompleter{reply: "answer", reasoning: "thinking"}
	session, _ := newPlainContextSession(t, store, completer, nil)
	session.EventBus = events.New()
	defer session.EventBus.Close()
	deltas := collectKind(session.EventBus, events.KindAssistant)
	session.EventBus.Flush()

	var out strings.Builder
	if _, err := session.SendUser(context.Background(), "hello", &out); err != nil {
		t.Fatal(err)
	}
	session.EventBus.Flush()

	if !completer.streamed() {
		t.Error("the provider was called without a StreamWriter; the reply would arrive in one lump")
	}
	if out.String() != "answer" {
		t.Errorf("caller writer got %q, want the streamed reply", out.String())
	}
	if len(deltas()) == 0 {
		t.Error("no live KindAssistant delta was published; the live-content path regressed")
	}
}

// TestPlainTurnWithoutReasoningPublishesNone keeps the publish conditional: a
// provider that reports no reasoning must not produce an empty thinking event
// for a consumer to render.
func TestPlainTurnWithoutReasoningPublishesNone(t *testing.T) {
	store, _ := openSharedContextStore(t)
	completer := &reasoningStreamCompleter{reply: "answer"}
	session, _ := newPlainContextSession(t, store, completer, nil)
	session.EventBus = events.New()
	defer session.EventBus.Close()
	thinking := collectKind(session.EventBus, events.KindThinking)
	session.EventBus.Flush()

	if _, err := session.SendUser(context.Background(), "hello", io.Discard); err != nil {
		t.Fatal(err)
	}
	session.EventBus.Flush()

	if got := thinking(); len(got) != 0 {
		t.Errorf("a turn with no reasoning published %d thinking events, want 0", len(got))
	}
}
