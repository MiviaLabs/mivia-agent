package conversation

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestSessionState_BackgroundPendingApprovalAndEdgeCases(t *testing.T) {
	dark, _, themes := themePair(t)

	// Test convID with nil conversation
	sNil := New(dark, theme.TierASCII, themes, nil, nil, 80, nil)
	if sNil.convID() != "default" {
		t.Errorf("expected default for nil conversation, got %q", sNil.convID())
	}

	// Test switchConversation(nil)
	sNil.switchConversation(nil)

	s, _, _, runner := setupTwoSessionScreen(t)

	// Start turn on A
	s = typeText(t, s, "Turn A")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Switch to B
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	// Send ToolPending event for background session A
	s2, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindToolPending,
			TurnID: "turn-sess-A",
			Body: uievent.ToolPendingBody{
				ToolCallID: "tc-pending",
				Name:       "write",
				Args:       map[string]any{"file": "test.txt"},
			},
		},
	})
	s = s2.(Screen)

	// Background session state should have approval set
	stA := s.sessions["sess-A"]
	if stA == nil || !stA.approval.Active() {
		t.Fatal("expected background session A to have active approval")
	}

	// Switch back to A (tests switching into already cached session in s.sessions)
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)

	if !s.approval.Active() {
		t.Fatal("expected foreground session A to carry active approval")
	}

	// Switch to a new session not in s.sessions with nil history
	sessC := &backgroundTestConversation{id: "sess-C", title: "Session C"}
	runner.convs["sess-C"] = sessC
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-C"))
	s = next.(Screen)

	if s.conv.ID() != "sess-C" {
		t.Errorf("expected session C, got %q", s.conv.ID())
	}

	// Event arriving for an unknown session ID or inactive session
	sUnk, cmdUnk := s.Update(uievent.EventMsg{
		SessionID: "unknown-session",
		Event:     uievent.Event{Kind: uievent.KindNotice, Body: uievent.NoticeBody{Text: "ignored"}},
	})
	if cmdUnk != nil || sUnk == nil {
		t.Errorf("expected no-op on unknown session event, got cmd=%v", cmdUnk)
	}
}
