package automation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// scriptedConversation sends exactly the events it is given, then closes
// the channel - letting a test drive sendTurnHeadless's KindError/
// KindNotice/KindTurnEnd branches directly with a controlled body shape,
// rather than the fixed KindTextDelta stream fakeConversation produces.
type scriptedConversation struct {
	events []uievent.Event
}

func (c *scriptedConversation) Send(ctx context.Context, in intent.Send) (ports.TurnHandle, error) {
	ch := make(chan uievent.Event, len(c.events))
	for _, ev := range c.events {
		ch <- ev
	}
	close(ch)
	return &fakeTurnHandle{ch: ch}, nil
}

func (c *scriptedConversation) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
func (c *scriptedConversation) History() []ports.Message             { return nil }
func (c *scriptedConversation) Model() ports.ModelInfo               { return ports.ModelInfo{} }
func (c *scriptedConversation) ContextUsage() ports.Usage            { return ports.Usage{} }
func (c *scriptedConversation) Title() string                        { return "" }
func (c *scriptedConversation) ID() string                           { return "scripted-conversation" }

// TestSendTurnHeadlessErrorBodySetsTurnErr pins the KindError branch when
// the event's Body is the expected uievent.ErrorBody shape: the returned
// turnErr must carry the body's Text verbatim.
func TestSendTurnHeadlessErrorBodySetsTurnErr(t *testing.T) {
	conv := &scriptedConversation{events: []uievent.Event{
		{Kind: uievent.KindError, Body: uievent.ErrorBody{Text: "boom"}},
	}}
	_, err := sendTurnHeadless(context.Background(), conv, "prompt", time.Second)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("sendTurnHeadless error = %v, want it to contain %q", err, "boom")
	}
}

// TestSendTurnHeadlessErrorKindNonErrorBodyFallback pins the KindError
// branch's else arm: a KindError event whose Body is NOT uievent.ErrorBody
// (a malformed/unexpected shape) still produces a named turnErr, with the
// generic fallback text rather than a body-derived message.
func TestSendTurnHeadlessErrorKindNonErrorBodyFallback(t *testing.T) {
	conv := &scriptedConversation{events: []uievent.Event{
		{Kind: uievent.KindError, Body: uievent.NoticeBody{Text: "not an error body"}},
	}}
	_, err := sendTurnHeadless(context.Background(), conv, "prompt", time.Second)
	if err == nil || !strings.Contains(err.Error(), "turn failed") {
		t.Fatalf("sendTurnHeadless error = %v, want generic 'turn failed' fallback", err)
	}
	if strings.Contains(err.Error(), "not an error body") {
		t.Fatalf("sendTurnHeadless error = %v, want it NOT to contain the non-ErrorBody text", err)
	}
}

// TestSendTurnHeadlessTurnEndErrorNoNoticeFallback pins the KindTurnEnd
// branch's own fallback when no prior KindError set turnErr and no
// KindNotice set lastNotice: the bare "turn ended in error" message.
func TestSendTurnHeadlessTurnEndErrorNoNoticeFallback(t *testing.T) {
	conv := &scriptedConversation{events: []uievent.Event{
		{Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "error"}},
	}}
	_, err := sendTurnHeadless(context.Background(), conv, "prompt", time.Second)
	if err == nil || err.Error() != "automation: turn ended in error" {
		t.Fatalf("sendTurnHeadless error = %v, want exactly %q", err, "automation: turn ended in error")
	}
}
