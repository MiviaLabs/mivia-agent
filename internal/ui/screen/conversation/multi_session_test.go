package conversation

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

type backgroundTestConversation struct {
	id      string
	title   string
	history []ports.Message
	events  chan uievent.Event
	sent    []string
}

func (c *backgroundTestConversation) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	c.sent = append(c.sent, in.Text)
	c.history = append(c.history, ports.Message{Role: "user", Text: in.Text, At: time.Now()})
	return &testTurnHandle{id: "turn-" + c.id, events: c.events}, nil
}

func (c *backgroundTestConversation) History() []ports.Message { return c.history }
func (c *backgroundTestConversation) Model() ports.ModelInfo {
	return ports.ModelInfo{Name: "model-" + c.id}
}
func (c *backgroundTestConversation) ContextUsage() ports.Usage { return ports.Usage{} }
func (c *backgroundTestConversation) Title() string             { return c.title }
func (c *backgroundTestConversation) ID() string                { return c.id }

type testTurnHandle struct {
	id     string
	events chan uievent.Event
}

func (h *testTurnHandle) ID() string                   { return h.id }
func (h *testTurnHandle) Events() <-chan uievent.Event { return h.events }
func (h *testTurnHandle) Cancel()                      {}

type testMultiSessionRunner struct {
	convs map[string]ports.Conversation
}

func (r *testMultiSessionRunner) Run(context.Context, string, string) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *testMultiSessionRunner) SelectModel(context.Context, string) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *testMultiSessionRunner) SelectAgent(context.Context, string) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *testMultiSessionRunner) SelectSession(_ context.Context, id string) ports.CommandOutcome {
	conv, ok := r.convs[id]
	if !ok {
		return ports.CommandOutcome{Err: "session not found"}
	}
	return ports.CommandOutcome{
		Conversation:    conv,
		ClearTranscript: true,
		Notice:          "Resumed session " + id,
	}
}

func setupTwoSessionScreen(t *testing.T) (Screen, *backgroundTestConversation, *backgroundTestConversation, *testMultiSessionRunner) {
	t.Helper()
	dark, _, themes := themePair(t)
	sessA := &backgroundTestConversation{id: "sess-A", title: "Session A", events: make(chan uievent.Event, 10)}
	sessB := &backgroundTestConversation{id: "sess-B", title: "Session B", events: make(chan uievent.Event, 10)}
	runner := &testMultiSessionRunner{convs: map[string]ports.Conversation{"sess-A": sessA, "sess-B": sessB}}

	s := New(dark, theme.TierASCII, themes, sessA, nil, 80, nil)
	s.SetCommandRunner(runner)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(Screen), sessA, sessB, runner
}

func TestMultiSession_BackgroundTurnAndSwitching(t *testing.T) {
	s, sessA, sessB, runner := setupTwoSessionScreen(t)

	// User sends a message in Session A
	s = typeText(t, s, "Run long task in A")
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.active == nil || cmd == nil {
		t.Fatal("expected active turn and event reader cmd on Session A")
	}

	// Switch to Session B using /resume outcome
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)
	if s.conv.ID() != "sess-B" || s.active != nil {
		t.Fatalf("conversation switch failed: id=%q, active=%v", s.conv.ID(), s.active)
	}

	// Emit an event for Session A while Session B is displayed
	evA := uievent.Event{
		Kind:   uievent.KindTextDelta,
		TurnID: "turn-sess-A",
		Body:   uievent.TextDeltaBody{Text: "Background output in A"},
	}
	next, _ = s.Update(sessionEventMsg{sessionID: "sess-A", event: evA})
	s = next.(Screen)
	if strings.Contains(s.View(), "Background output in A") {
		t.Errorf("Session B view was polluted with Session A event:\n%s", s.View())
	}

	// User sends a message in Session B
	s = typeText(t, s, "Question in B")
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if len(sessB.sent) != 1 || sessB.sent[0] != "Question in B" {
		t.Fatalf("Session B sent text mismatch: %+v", sessB.sent)
	}

	// Switch back to Session A
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)
	if s.conv.ID() != "sess-A" || !strings.Contains(s.View(), "Background output in A") {
		t.Errorf("Session A view missing background output accumulated while away:\n%s", s.View())
	}
}

func TestMultiSession_ApprovalInSessionA_DoesNotBlockSessionB(t *testing.T) {
	s, _, sessB, runner := setupTwoSessionScreen(t)

	// Send message in Session A to start a turn
	s = typeText(t, s, "Edit file")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Session A triggers a tool pending approval
	pendingEv := uievent.Event{
		Kind:   uievent.KindToolPending,
		TurnID: "turn-sess-A",
		Body: uievent.ToolPendingBody{
			ToolCallID: "call-1",
			Name:       "write_file",
			Args:       map[string]any{"path": "main.go"},
		},
	}
	next, _ = s.Update(sessionEventMsg{sessionID: "sess-A", event: pendingEv})
	s = next.(Screen)
	if !s.approval.Active() {
		t.Fatal("expected approval to be active on Session A")
	}

	// Switch to Session B
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)
	if s.approval.Active() {
		t.Fatal("expected approval to NOT be active on Session B")
	}

	// User should be able to type and send freely in Session B
	s = typeText(t, s, "Hello in B")
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if len(sessB.sent) != 1 || sessB.sent[0] != "Hello in B" {
		t.Fatalf("Session B should have sent message, got %+v", sessB.sent)
	}

	// Switch back to Session A
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)
	if !s.approval.Active() {
		t.Fatal("expected approval to still be active when switching back to Session A")
	}
}

func TestMultiSession_TurnEndCleansUpBackgroundSession(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	// Start turn on A
	s = typeText(t, s, "Task in A")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Switch to B
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	// Session A completes in background
	next, _ = s.Update(sessionTurnEndedMsg{sessionID: "sess-A"})
	s = next.(Screen)

	// Switch back to A
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)

	// Session A is no longer active
	if s.active != nil {
		t.Errorf("expected Session A active handle to be cleared on turn end, got %v", s.active)
	}
}
