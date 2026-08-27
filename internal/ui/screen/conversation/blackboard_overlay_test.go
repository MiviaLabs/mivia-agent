package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func TestConversation_BlackboardOverlay_TriggerAndTabs(t *testing.T) {
	s := sized(t, 2)

	// Post finding event into conversation screen
	ev := uievent.Event{
		Kind:   uievent.KindToolStart,
		TurnID: "turn-1",
		Seq:    1,
		At:     time.Now(),
		Body: uievent.ToolStartBody{
			ToolCallID: "call-1",
			Name:       "post_message",
			Args: map[string]any{
				"kind": "finding",
				"body": "Discovered mutex contention at storage layer",
			},
		},
	}
	next, _ := s.Update(uievent.EventMsg{Event: ev})
	s = next.(Screen)

	// Post direct message event
	evMsg := uievent.Event{
		Kind:   uievent.KindToolStart,
		TurnID: "turn-1",
		Seq:    2,
		At:     time.Now(),
		Body: uievent.ToolStartBody{
			ToolCallID: "call-2",
			Name:       "send_to_task",
			Args: map[string]any{
				"task_id": "builder-task-1",
				"action":  "steer",
				"body":    "Focus on fixing storage mutex",
			},
		},
	}
	next, _ = s.Update(uievent.EventMsg{Event: evMsg})
	s = next.(Screen)

	// Open blackboard via F3
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyF3})
	if !s.blackboard.Active() {
		t.Fatal("expected blackboard overlay to be active on F3")
	}

	// Verify Finding is rendered in Findings tab
	view := s.blackboard.View()
	if !strings.Contains(view, "Discovered mutex contention") {
		t.Errorf("expected finding in view, got %q", view)
	}

	// Press Tab to switch to Messages tab
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyTab})
	view = s.blackboard.View()
	if !strings.Contains(view, "Focus on fixing storage mutex") {
		t.Errorf("expected message in messages tab view, got %q", view)
	}

	// Press Esc to close
	s, _ = press(t, s, tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.blackboard.Active() {
		t.Fatal("expected blackboard overlay to close on Esc")
	}
}

func TestConversation_BlackboardOverlay_SlashCommand(t *testing.T) {
	s := sized(t, 2)
	s.blackboard.AddFinding("researcher", "API rate limit is 100 req/sec", nil)

	s, _ = sendLine(t, s, "/blackboard")
	if !s.blackboard.Active() {
		t.Fatal("expected /blackboard to activate overlay")
	}
	if !strings.Contains(s.blackboard.View(), "API rate limit is 100 req/sec") {
		t.Errorf("expected finding in view, got %q", s.blackboard.View())
	}
}
