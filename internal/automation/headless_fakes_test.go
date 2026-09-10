package automation

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// fakeConversation is a minimal ports.Conversation test double that
// reproduces the exact hazard D5 documents: Send holds turnMu for the
// life of the turn's producer goroutine, and that goroutine's channel
// send blocks (unparked only by the consumer draining or by the turn's
// ctx being cancelled) - the same shape conversation.go/turn_stream.go
// use for the real implementation.
type fakeConversation struct {
	turnMu sync.Mutex
}

func newFakeConversation() *fakeConversation { return &fakeConversation{} }

const fakeTurnEventCount = 5

// Send starts a turn producing fakeTurnEventCount events on an
// unbuffered channel (so every send blocks until read - the worst case
// the real buffered-at-32 channel degrades to once the buffer is full)
// and holds turnMu until the producer goroutine exits, exactly as
// conversation.go's real Send does.
func (c *fakeConversation) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.turnMu.Lock()
	ch := make(chan uievent.Event)
	go func() {
		defer c.turnMu.Unlock()
		defer close(ch)
		for i := 0; i < fakeTurnEventCount; i++ {
			ev := uievent.Event{Kind: uievent.KindTextDelta, Body: uievent.TextDeltaBody{Text: "x"}}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return &fakeTurnHandle{ch: ch}, nil
}

func (c *fakeConversation) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
func (c *fakeConversation) History() []ports.Message             { return nil }
func (c *fakeConversation) Model() ports.ModelInfo               { return ports.ModelInfo{} }
func (c *fakeConversation) ContextUsage() ports.Usage            { return ports.Usage{} }
func (c *fakeConversation) Title() string                        { return "" }
func (c *fakeConversation) ID() string                           { return "fake-conversation" }

// waitUnlocked returns a channel that closes once turnMu is acquirable,
// bounded by timeout - a bounded proxy for "the producer goroutine has
// exited (or never will within the wait)".
func (c *fakeConversation) waitUnlocked(timeout time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		c.turnMu.Lock()
		c.turnMu.Unlock()
		close(done)
	}()
	result := make(chan struct{})
	go func() {
		select {
		case <-done:
			close(result)
		case <-time.After(timeout):
			// leave result unclosed: caller's own select times out too.
		}
	}()
	return result
}

type fakeTurnHandle struct {
	ch chan uievent.Event
}

func (h *fakeTurnHandle) ID() string                        { return "fake-turn" }
func (h *fakeTurnHandle) Events() <-chan uievent.Event      { return h.ch }
func (h *fakeTurnHandle) Cancel()                           {}
func (h *fakeTurnHandle) CancelToolCall(callID string) bool { return false }

// fakeSpawner satisfies SessionSpawner by always returning the same
// conv, so a test can drive one conversation across multiple calls and
// observe whether a prior turn's wedge affects a later Send.
type fakeSpawner struct {
	conv ports.Conversation
}

func (f *fakeSpawner) CreateFreshInDir(bind func(*chat.Session) (string, error), dir string) (ports.Conversation, error) {
	return f.conv, nil
}

// SetApprovalOverride is a no-op for headless.go's wedge tests: none of
// them exercise D8's unattended approval override, only the drain-to-
// close/spawn/send plumbing.
func (f *fakeSpawner) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	return nil
}
