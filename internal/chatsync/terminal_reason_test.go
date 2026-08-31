package chatsync

import (
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// Contract requirement 3 (2026-08-31-chat-sync-event-contract.md:132):
// "Every enumerated string is open - status, phase, reason need a default
// branch, and an absent value must read as the benign value, not as failure."
// Combined with the one-terminal-per-turn rule, that gives three obligations
// this file pins:
//
//  1. An ABSENT turn-end reason projects as ordinary completion.
//  2. An UNRECOGNISED turn-end reason projects as ordinary completion and is
//     carried verbatim - never rewritten into turn.failed.
//  3. A turn emits exactly ONE terminal, never both turn.ended and
//     turn.failed, in either arrival order.
//
// Named mutations these kill:
//
//	M17 - delete the `if reason == "" { reason = "completed" }` default in
//	      projectTurnEnd, so an absent reason reaches the wire as "".
//	M35 - route a reason outside a recognised set to TypeTurnFailed, so an
//	      unknown reason is rewritten into a failure.
func startTurn(t *testing.T, p *Projector, turnID string) {
	t.Helper()
	got := p.Project(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    turnID,
		Detail:    "user text",
		Timestamp: time.Now(),
	})
	if len(got) != 1 {
		t.Fatalf("turn_start produced %d events, want 1", len(got))
	}
}

func TestProjectorTurnEndReasonSetIsOpen(t *testing.T) {
	cases := []struct {
		name       string
		detail     string
		wantReason string
	}{
		{name: "absent reason reads as ordinary completion", detail: "", wantReason: "completed"},
		{name: "recognised reason preserved", detail: "completed", wantReason: "completed"},
		{name: "unrecognised reason preserved verbatim", detail: "quiesced_by_operator", wantReason: "quiesced_by_operator"},
		{name: "future reason preserved verbatim", detail: "max_tokens", wantReason: "max_tokens"},
		{name: "failure-shaped word is still not a failure", detail: "error", wantReason: "error"},
		{name: "cancelled is still not a failure", detail: "cancelled", wantReason: "cancelled"},
		{name: "oversized reason is truncated, not reclassified", detail: strings.Repeat("z", BudgetShortField+40), wantReason: strings.Repeat("z", BudgetShortField)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjector("sess-1", 0, ProjectorOptions{})
			startTurn(t, p, "turn:1")

			got := p.Project(events.Event{
				Kind:      events.KindTurnEnd,
				SessionID: "sess-1",
				TurnID:    "turn:1",
				Detail:    tc.detail,
				Timestamp: time.Now(),
			})
			if len(got) != 1 {
				t.Fatalf("turn_end produced %d events, want exactly 1", len(got))
			}
			if got[0].Type != TypeTurnEnded {
				t.Fatalf("type = %q, want %q; an unrecognised or absent terminal reason "+
					"must read as ORDINARY COMPLETION and must never be rewritten into a failure",
					got[0].Type, TypeTurnEnded)
			}
			payload, ok := got[0].Payload.(*TurnEndedPayload)
			if !ok {
				t.Fatalf("payload type = %T, want *TurnEndedPayload", got[0].Payload)
			}
			if payload.Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", payload.Reason, tc.wantReason)
			}
		})
	}
}

// TestProjectorTurnEndReasonNeverEmptyOnWire is the narrow M17 kill: the wire
// field is required (`json:"reason"`, no omitempty), so an empty string is a
// value the viewer must interpret, and the contract says the benign default is
// what an absent value means.
func TestProjectorTurnEndReasonNeverEmptyOnWire(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})
	startTurn(t, p, "turn:1")

	got := p.Project(events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
	})
	if len(got) != 1 {
		t.Fatalf("turn_end produced %d events, want 1", len(got))
	}
	payload := got[0].Payload.(*TurnEndedPayload)
	if payload.Reason == "" {
		t.Fatal(`reason = ""; an absent terminal reason must be defaulted to the ` +
			`benign value ("completed") before it reaches the wire, not passed through empty`)
	}
}

// TestProjectorExactlyOneTerminalPerTurn covers "exactly one terminal per
// turn, never both" in BOTH arrival orders. Ordering between kinds is not
// guaranteed (per-kind subscriptions), so neither order may mint a second
// terminal.
func TestProjectorExactlyOneTerminalPerTurn(t *testing.T) {
	errEvent := events.Event{
		Kind:      events.KindError,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "provider refused",
		Timestamp: time.Now(),
	}
	endEvent := events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "completed",
		Timestamp: time.Now(),
	}

	cases := []struct {
		name     string
		first    events.Event
		second   events.Event
		wantType string
	}{
		{name: "turn_end wins, later error dropped", first: endEvent, second: errEvent, wantType: TypeTurnEnded},
		{name: "error wins, later turn_end dropped", first: errEvent, second: endEvent, wantType: TypeTurnFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProjector("sess-1", 0, ProjectorOptions{})
			startTurn(t, p, "turn:1")

			first := p.Project(tc.first)
			if len(first) != 1 {
				t.Fatalf("first terminal produced %d events, want 1", len(first))
			}
			if first[0].Type != tc.wantType {
				t.Fatalf("first terminal type = %q, want %q", first[0].Type, tc.wantType)
			}

			second := p.Project(tc.second)
			if len(second) != 0 {
				t.Fatalf("second terminal produced %d events (%q), want 0: a turn "+
					"carries exactly one terminal, never both",
					len(second), second[0].Type)
			}
		})
	}
}

// TestProjectorErrorTerminalStillFails guards the tests above from being
// satisfied by a projector that simply never emits turn.failed: a real error
// event must still project as a failure terminal.
func TestProjectorErrorTerminalStillFails(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})
	startTurn(t, p, "turn:1")

	got := p.Project(events.Event{
		Kind:      events.KindError,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "provider refused",
		Timestamp: time.Now(),
	})
	if len(got) != 1 {
		t.Fatalf("error produced %d events, want 1", len(got))
	}
	if got[0].Type != TypeTurnFailed {
		t.Fatalf("type = %q, want %q", got[0].Type, TypeTurnFailed)
	}
	payload := got[0].Payload.(*TurnFailedPayload)
	if payload.Message != "provider refused" {
		t.Errorf("message = %q, want %q", payload.Message, "provider refused")
	}
}
