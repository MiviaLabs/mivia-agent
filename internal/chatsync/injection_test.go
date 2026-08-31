package chatsync

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Settled decision 7 makes internal/chatsync a leaf. Two edges used to break
// that: chat.TurnErrorMessage (the only use of internal/chat, which drags in
// ~47 internal packages) and tools.RedactToolArgs() (process-global mutable
// state). Both are now injected on ProjectorOptions. These tests pin the
// injected behaviour AND the fail-closed defaults; the structural half of the
// gate is the deny pair in .mivia/policy/import-layers.json.

func TestShouldIncludeToolIOComposesAsAnd(t *testing.T) {
	cases := []struct {
		includeToolIO  bool
		redactToolArgs bool
		want           bool
	}{
		{includeToolIO: false, redactToolArgs: false, want: false},
		{includeToolIO: false, redactToolArgs: true, want: false},
		{includeToolIO: true, redactToolArgs: true, want: false},
		{includeToolIO: true, redactToolArgs: false, want: true},
	}
	for _, tc := range cases {
		opts := ProjectorOptions{IncludeToolIO: tc.includeToolIO, RedactToolArgs: tc.redactToolArgs}
		if got := shouldIncludeToolIO(opts); got != tc.want {
			t.Errorf("shouldIncludeToolIO(IncludeToolIO=%v, RedactToolArgs=%v) = %v, want %v",
				tc.includeToolIO, tc.redactToolArgs, got, tc.want)
		}
	}
}

// TestProjectorRedactToolArgsWithheldEndToEnd drives the composition through
// the real projection path, not just the predicate, and asserts the withheld
// field is ABSENT and NAMED (the three-states rule), never silently empty.
func TestProjectorRedactToolArgsWithheldEndToEnd(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{IncludeToolIO: true, RedactToolArgs: true})

	got := p.Project(events.Event{
		Kind:       events.KindSubagentStart,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: "call_1",
		Name:       "task",
		Input:      "argv the user must not see",
		Timestamp:  time.Now(),
	})
	if len(got) != 1 {
		t.Fatalf("produced %d events, want 1", len(got))
	}
	payload := got[0].Payload.(*SubagentToolStartedPayload)
	if payload.Input != "" {
		t.Errorf("input = %q, want '' because RedactToolArgs is set", payload.Input)
	}
	if len(payload.Redacted) != 1 || payload.Redacted[0] != "input" {
		t.Errorf("redacted = %v, want [\"input\"]", payload.Redacted)
	}
	if payload.InputBytes != len("argv the user must not see") {
		t.Errorf("input_bytes = %d, want the real byte length", payload.InputBytes)
	}
}

func TestProjectorErrorMessageClassifierIsInjected(t *testing.T) {
	var seen error
	p := NewProjector("sess-1", 0, ProjectorOptions{
		ErrorMessage: func(err error) string {
			seen = err
			return "classified by the host"
		},
	})
	startTurn(t, p, "turn:1")

	sentinel := errors.New("boom: POST /v1/messages body=hunter2")
	got := p.Project(events.Event{
		Kind:      events.KindError,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Err:       sentinel,
		Timestamp: time.Now(),
	})
	if len(got) != 1 {
		t.Fatalf("produced %d events, want 1", len(got))
	}
	if !errors.Is(seen, sentinel) {
		t.Errorf("classifier saw %v, want the original error", seen)
	}
	if msg := got[0].Payload.(*TurnFailedPayload).Message; msg != "classified by the host" {
		t.Errorf("message = %q, want the injected classifier's output", msg)
	}
}

// TestDefaultErrorMessageIsContentFree is the fail-closed proof: with no
// classifier injected, the wire must not carry err.Error(). Provider and tool
// error text can quote the request that produced it (DC-14).
func TestDefaultErrorMessageIsContentFree(t *testing.T) {
	secret := "hunter2-Authorization-Bearer-abc123"
	err := errors.New("upstream rejected: " + secret)

	if got := defaultErrorMessage(nil); got != "" {
		t.Errorf("defaultErrorMessage(nil) = %q, want \"\"", got)
	}

	p := NewProjector("sess-1", 0, ProjectorOptions{}) // ErrorMessage nil
	startTurn(t, p, "turn:1")
	got := p.Project(events.Event{
		Kind:      events.KindError,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Err:       err,
		Timestamp: time.Now(),
	})
	if len(got) != 1 {
		t.Fatalf("produced %d events, want 1", len(got))
	}
	msg := got[0].Payload.(*TurnFailedPayload).Message
	if strings.Contains(msg, secret) || strings.Contains(msg, "upstream rejected") {
		t.Fatalf("message = %q; the default classifier leaked err.Error() onto the wire", msg)
	}
	if msg != "chat turn failed" {
		t.Errorf("message = %q, want %q", msg, "chat turn failed")
	}
}

// TestErrorEventWithoutErrUsesDetail keeps the Detail branch covered: an error
// event with no Err carries the already-safe Detail string.
func TestErrorEventWithoutErrUsesDetail(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{
		ErrorMessage: func(error) string { return "should not be called" },
	})
	startTurn(t, p, "turn:1")

	got := p.Project(events.Event{
		Kind:      events.KindError,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "tool refused",
		Timestamp: time.Now(),
	})
	if len(got) != 1 {
		t.Fatalf("produced %d events, want 1", len(got))
	}
	if msg := got[0].Payload.(*TurnFailedPayload).Message; msg != "tool refused" {
		t.Errorf("message = %q, want %q", msg, "tool refused")
	}
}
