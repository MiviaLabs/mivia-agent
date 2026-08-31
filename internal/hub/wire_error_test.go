package hub

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestToWireNeverSerializesRawErrorText is the privacy gate on the hub's wire.
//
// toWire used to set ErrorText from err.Error() verbatim. Provider and tool
// error text can quote the request that produced it (DC-14), so that put a
// user's own prompt on a cross-process socket - and it did so at the boundary
// that reaches ANOTHER process, while this process's own NDJSON output
// deliberately emitted a classified string instead. The wire was the leakier of
// the two.
//
// The secret here is a marker no classified message can contain, so the
// assertion is about the mechanism (raw text reaching the wire) rather than
// about any one error's wording.
func TestToWireNeverSerializesRawErrorText(t *testing.T) {
	const secret = "SECRET-PROMPT-TEXT-9f3a"

	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "provider error quoting the request",
			err:  fmt.Errorf("provider rejected request: %s", secret),
			want: "chat turn failed",
		},
		{
			name: "wrapped persistence sentinel",
			err:  fmt.Errorf("saving turn %s: %w", secret, chat.ErrPersistence),
			want: "chat turn failed: could not persist session state",
		},
		{
			name: "wrapped staleness sentinel",
			err:  fmt.Errorf("turn %s superseded: %w", secret, chat.ErrStaleOperation),
			want: "chat turn failed: superseded by a newer turn",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := toWire(events.Event{
				Kind: events.KindError, SessionID: "s1", TurnID: "turn:1", Err: tc.err,
			})
			if strings.Contains(w.ErrorText, secret) {
				t.Fatalf("raw error text reached the wire: %q", w.ErrorText)
			}
			if w.ErrorText != tc.want {
				t.Fatalf("ErrorText = %q, want %q", w.ErrorText, tc.want)
			}
		})
	}
}

// TestToWireLeavesErrorTextEmptyWithoutAnError guards the other direction: the
// classifier returns a non-empty string for every non-nil error, so calling it
// unconditionally would stamp "chat turn failed" onto every relayed delta.
func TestToWireLeavesErrorTextEmptyWithoutAnError(t *testing.T) {
	w := toWire(events.Event{Kind: events.KindAssistant, SessionID: "s1", TurnID: "turn:1", Content: "hi"})
	if w.ErrorText != "" {
		t.Fatalf("ErrorText = %q for an event with no error, want empty", w.ErrorText)
	}
}

// TestFromWireRebuildsAnErrorFromTheClassifiedText pins that the receiving side
// still gets a usable error value - classification must not silently turn a
// failed turn into a successful-looking one two processes away.
func TestFromWireRebuildsAnErrorFromTheClassifiedText(t *testing.T) {
	ev := fromWire(toWire(events.Event{
		Kind: events.KindError, SessionID: "s1", TurnID: "turn:1",
		Err: errors.New("some provider detail"),
	}))
	if ev.Err == nil {
		t.Fatal("fromWire dropped the error entirely; the receiver cannot tell the turn failed")
	}
	if ev.Err.Error() != "chat turn failed" {
		t.Fatalf("reconstructed error = %q, want the classified message", ev.Err)
	}
}
