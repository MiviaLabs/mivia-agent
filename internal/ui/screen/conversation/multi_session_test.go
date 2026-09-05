package conversation

import (
	"context"
	"errors"
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

func (c *backgroundTestConversation) History() []ports.Message             { return c.history }
func (c *backgroundTestConversation) ActiveTurn() (ports.TurnHandle, bool) { return nil, false }
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
func (h *testTurnHandle) CancelToolCall(string) bool   { return false }

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
func (r *testMultiSessionRunner) SelectEffort(context.Context, string) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *testMultiSessionRunner) SessionActive(string) bool {
	return false
}
func (r *testMultiSessionRunner) CompleteLogin(context.Context, string, []byte) ports.CommandOutcome {
	return ports.CommandOutcome{}
}
func (r *testMultiSessionRunner) StartInWorktree(context.Context, ports.SessionSummary) ports.CommandOutcome {
	return ports.CommandOutcome{}
}

func (r *testMultiSessionRunner) StartInNewWorktree(context.Context, string) ports.CommandOutcome {
	return ports.CommandOutcome{}
}

func (r *testMultiSessionRunner) ResumeInWorktree(context.Context, ports.SessionSummary) ports.CommandOutcome {
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
	s, _, sessB, runner := setupTwoSessionScreen(t)

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
	next, _ = s.Update(uievent.EventMsg{SessionID: "sess-A", Event: evA})
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
	next, _ = s.Update(uievent.EventMsg{SessionID: "sess-A", Event: pendingEv})
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
	next, _ = s.Update(turnEndedMsg{sessionID: "sess-A"})
	s = next.(Screen)

	// Switch back to A
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)

	// Session A is no longer active
	if s.active != nil {
		t.Errorf("expected Session A active handle to be cleared on turn end, got %v", s.active)
	}
}

func TestMultiSession_BackgroundToolEventsUpdateState(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	// Start turn on A
	s = typeText(t, s, "Task in A")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Switch to B
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	// Emit ToolStart, ToolOutput, ToolEnd, TurnEnd for Session A
	s2, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindToolStart,
			TurnID: "turn-sess-A",
			Body:   uievent.ToolStartBody{Name: "search", Args: map[string]any{"q": "go"}},
		},
	})
	s = s2.(Screen)

	s3, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindToolOutput,
			TurnID: "turn-sess-A",
			Body: uievent.ToolOutputBody{
				ToolCallID: "tc-1",
				Progress:   &uievent.Progress{Status: "indexing", Step: 1, TotalSteps: 2},
			},
		},
	})
	s = s3.(Screen)

	s4, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindToolEnd,
			TurnID: "turn-sess-A",
			Body: uievent.ToolEndBody{
				Diff: &uievent.Diff{Path: "main.go", Added: 2, Removed: 1},
			},
		},
	})
	s = s4.(Screen)

	s5, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindTurnEnd,
			TurnID: "turn-sess-A",
			Body:   uievent.TurnEndBody{Reason: "completed"},
		},
	})
	s = s5.(Screen)

	// Switch back to A
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)
	if s.conv.ID() != "sess-A" {
		t.Fatalf("expected to switch back to sess-A, got %q", s.conv.ID())
	}
}

func TestMultiSession_SwitchConversationReflowsDimensions(t *testing.T) {
	th := theme.Theme{Name: "test"}
	convA := &backgroundTestConversation{
		id:    "sess-A",
		title: "Session A",
		history: []ports.Message{
			{Role: "user", Text: "Hello in A", At: time.Now()},
			{Role: "assistant", Text: "Response in A", At: time.Now()},
		},
		events: make(chan uievent.Event, 10),
	}
	convB := &backgroundTestConversation{
		id:    "sess-B",
		title: "Session B",
		history: []ports.Message{
			{Role: "user", Text: "Hello in B", At: time.Now()},
			{Role: "assistant", Text: "Response in B", At: time.Now()},
		},
		events: make(chan uievent.Event, 10),
	}

	runner := &testMultiSessionRunner{
		convs: map[string]ports.Conversation{
			"sess-A": convA,
			"sess-B": convB,
		},
	}

	screen := New(th, theme.TierTrueColor, []theme.Theme{th}, convA, nil, 100, func() time.Time { return time.Time{} })
	screen.SetCommandRunner(runner)

	// Set window size to 120 width x 40 height
	s1, _ := screen.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	s := s1.(Screen)

	wantChatWidth := 120 - 2 // contentWidth(120) = 118
	if s.transcript.Width() != wantChatWidth {
		t.Fatalf("initial transcript width = %d, want %d", s.transcript.Width(), wantChatWidth)
	}

	// Switch to sess-B
	next, _ := s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	if s.conv.ID() != "sess-B" {
		t.Fatalf("expected active conv to be sess-B, got %q", s.conv.ID())
	}
	if s.transcript.Width() != wantChatWidth {
		t.Errorf("after switch to sess-B, transcript.Width() = %d, want %d", s.transcript.Width(), wantChatWidth)
	}
	if s.transcript.Height() != s.transcriptHeight() {
		t.Errorf("after switch to sess-B, transcript.Height() = %d, want %d", s.transcript.Height(), s.transcriptHeight())
	}

	view := s.View()
	if !strings.Contains(view, "Hello in B") || !strings.Contains(view, "Response in B") {
		t.Errorf("view after switch missing sess-B messages: %s", view)
	}
}

func TestMultiSession_BackgroundSubagentTurnEndReconcilesStatus(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	// Start turn on A
	s = typeText(t, s, "Task in A")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Switch to B
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	// Emit subagent tool start on Session A in background
	s2, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindToolStart,
			TurnID: "turn-sess-A",
			Body:   uievent.ToolStartBody{ToolCallID: "sa-1", Name: "invoke_subagent"},
		},
	})
	s = s2.(Screen)

	// Turn on Session A ends with cancelled
	s3, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindTurnEnd,
			TurnID: "turn-sess-A",
			Body:   uievent.TurnEndBody{Reason: "cancelled"},
		},
	})
	s = s3.(Screen)

	// Switch back to A and check panel subagents
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)

	if len(s.panel.agents) != 1 {
		t.Fatalf("expected 1 subagent on session A, got %d", len(s.panel.agents))
	}
	if s.panel.agents[0].Status != "cancelled" {
		t.Errorf("expected subagent status 'cancelled', got %q", s.panel.agents[0].Status)
	}
}

func TestMultiSession_BackgroundSubagentTurnEndedMsgReconcilesStatus(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	// Start turn on A
	s = typeText(t, s, "Task in A")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Switch to B
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	// Emit subagent tool start on Session A in background
	s2, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindToolStart,
			TurnID: "turn-sess-A",
			Body:   uievent.ToolStartBody{ToolCallID: "sa-2", Name: "invoke_subagent"},
		},
	})
	s = s2.(Screen)

	// Channel closes abruptly -> turnEndedMsg dispatched for Session A
	s3, _ := s.Update(turnEndedMsg{sessionID: "sess-A"})
	s = s3.(Screen)

	// Switch back to A and check panel subagents
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)

	if len(s.panel.agents) != 1 {
		t.Fatalf("expected 1 subagent on session A, got %d", len(s.panel.agents))
	}
	if s.panel.agents[0].Status != "interrupted" {
		t.Errorf("expected subagent status 'interrupted', got %q", s.panel.agents[0].Status)
	}
}

func TestMultiSession_TurnEndBodyThenTurnEndedMsgPreservesStatus(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	// Start turn on A
	s = typeText(t, s, "Task in A")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Switch to B
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	// Emit subagent tool start on Session A in background
	s2, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindToolStart,
			TurnID: "turn-sess-A",
			Body:   uievent.ToolStartBody{ToolCallID: "sa-3", Name: "invoke_subagent"},
		},
	})
	s = s2.(Screen)

	// TurnEndBody sent with completed
	s3, _ := s.Update(uievent.EventMsg{
		SessionID: "sess-A",
		Event: uievent.Event{
			Kind:   uievent.KindTurnEnd,
			TurnID: "turn-sess-A",
			Body:   uievent.TurnEndBody{Reason: "completed"},
		},
	})
	s = s3.(Screen)

	// Subsequent turnEndedMsg received when channel closes
	s4, _ := s.Update(turnEndedMsg{sessionID: "sess-A"})
	s = s4.(Screen)

	// Switch back to A and check panel subagents
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)

	if len(s.panel.agents) != 1 {
		t.Fatalf("expected 1 subagent on session A, got %d", len(s.panel.agents))
	}
	if s.panel.agents[0].Status != "completed" {
		t.Errorf("expected subagent status 'completed', got %q", s.panel.agents[0].Status)
	}
}

func TestMultiSession_QueueIsolationAcrossSessions(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	// Start active turn on A
	s = typeText(t, s, "Turn A")
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Queue message on A
	s = typeText(t, s, "Queued on A")
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	if len(s.queue) != 1 || s.queue[0] != "Queued on A" {
		t.Fatalf("expected queue on A = [\"Queued on A\"], got %v", s.queue)
	}

	// Switch to Session B
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	if len(s.queue) != 0 {
		t.Errorf("expected empty queue on fresh Session B, got %v", s.queue)
	}

	// Switch back to Session A
	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)

	if len(s.queue) != 1 || s.queue[0] != "Queued on A" {
		t.Errorf("expected restored queue on Session A = [\"Queued on A\"], got %v", s.queue)
	}
}

// liveUsage is one turn's provider-reported accounting, so it belongs to that
// turn's session. Held on the Screen and never carried in sessionState, a
// reading from session A's running turn kept overriding the top bar after a
// switch: session B showed A's context fill and A's spend, and A's TurnEnd
// arrives on the background path which cannot clear the screen-global field -
// so a session the user never sent a turn to never corrects itself.
func TestMultiSession_LiveUsageDoesNotFollowTheSwitch(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)

	next, _ := s.Update(uievent.EventMsg{SessionID: "sess-A", Event: uievent.Event{
		Kind:   uievent.KindUsage,
		TurnID: "turn-sess-A",
		Body:   uievent.UsageBody{InputTokens: 123456, CostUSD: 9.99},
	}})
	s = next.(Screen)
	if s.liveUsage == nil {
		t.Fatal("precondition: session A's usage event was not recorded")
	}

	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)
	if got := s.topbar.Usage().InputTokens; got == 123456 {
		t.Errorf("session B's top bar reports session A's %d input tokens", got)
	}
	if got := s.topbar.Usage().CostUSD; got == 9.99 {
		t.Errorf("session B's top bar reports session A's $%.2f spend", got)
	}

	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)
	if got := s.topbar.Usage().InputTokens; got != 123456 {
		t.Errorf("switching back to A lost its own live usage: got %d, want 123456", got)
	}
}

// A remote input for an unmounted session is buffered while the mount runs.
// When the mount fails the buffer was discarded with a notice that named only
// the error, so the user's message vanished with no way to see what was lost.
func TestRemoteInputMountFailureReportsTheDroppedInput(t *testing.T) {
	s, _, _, _ := setupTwoSessionScreen(t)
	s.mounting = map[string][]ports.RemoteInputEvent{
		"sess-gone": {{SessionID: "sess-gone", Body: "please run the migration"}},
	}
	next, _ := s.Update(sessionMountedMsg{sessionID: "sess-gone", err: errMountFailed})
	s = next.(Screen)
	view := s.View()
	if !strings.Contains(view, "please run the migration") {
		t.Errorf("the dropped remote input is not reported anywhere the user can see it:\n%s", view)
	}
}

var errMountFailed = errors.New("worktree was removed")

// backgroundEvent feeds one event for a session that is NOT in the
// foreground, i.e. through sessionState.handleTurnEvent.
func backgroundEvent(t *testing.T, s Screen, sessionID string, body uievent.Body) Screen {
	t.Helper()
	next, _ := s.Update(uievent.EventMsg{SessionID: sessionID, Event: uievent.Event{TurnID: "turn-" + sessionID, Body: body}})
	return next.(Screen)
}

// The background handler is a partial copy of the foreground one, and every
// case it omits is state the owning session never recovers. Its TurnEnd left
// the snapshotted liveUsage set, so switching back showed a stale mid-turn
// reading (the very symptom the per-session enrolment was meant to remove);
// its ToolEnd matched dispatch rows by call id, which never matches the
// "callID:taskID" rows a dispatch group registers, so finished subagents
// stayed "running"; and its ToolStart recorded nothing on the blackboard, so
// cross-agent messages and findings raised while backgrounded were lost.
func TestMultiSession_BackgroundPathKeepsSessionStateWhole(t *testing.T) {
	s, _, _, runner := setupTwoSessionScreen(t)
	const call = "tc-dispatch"
	args := map[string]any{"tasks": []any{map[string]any{"id": "t1", "agent": "alpha"}}}

	next, _ := s.Update(uievent.EventMsg{SessionID: "sess-A", Event: uievent.Event{
		Kind: uievent.KindUsage, TurnID: "turn-sess-A",
		Body: uievent.UsageBody{InputTokens: 4242, CostUSD: 1.25},
	}})
	s = next.(Screen)
	next, _ = s.Update(uievent.EventMsg{SessionID: "sess-A", Event: uievent.Event{
		Kind: uievent.KindToolStart, TurnID: "turn-sess-A",
		Body: uievent.ToolStartBody{ToolCallID: call, Name: "dispatch_tasks", Args: args},
	}})
	s = next.(Screen)

	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-B"))
	s = next.(Screen)

	// Everything below reaches session A through the BACKGROUND path.
	s = backgroundEvent(t, s, "sess-A", uievent.ToolStartBody{ToolCallID: "tc-msg", Name: "post_message",
		Args: map[string]any{"kind": "finding", "body": "a finding raised while backgrounded"}})
	s = backgroundEvent(t, s, "sess-A", uievent.ToolEndBody{ToolCallID: call, OK: true,
		Result: `{"tasks":[{"id":"t1","status":"succeeded"}]}`})

	// Asserted BEFORE TurnEnd on purpose: reconcileTerminal stamps every
	// non-terminal row with the TURN's reason, which masks a group whose
	// rows were never resolved from the call's own result.
	for _, row := range s.sessions["sess-A"].panel.agents {
		if row.Status != "succeeded" {
			t.Errorf("dispatch row %q = %q after its call ended, want the per-task status from the result", row.ID, row.Status)
		}
	}

	s = backgroundEvent(t, s, "sess-A", uievent.TurnEndBody{Reason: "completed"})

	next, _ = s.applyCommandOutcome(runner.SelectSession(context.Background(), "sess-A"))
	s = next.(Screen)

	if s.liveUsage != nil {
		t.Errorf("session A's own TurnEnd did not clear its live usage: %+v", *s.liveUsage)
	}
	if got := s.topbar.Usage().InputTokens; got == 4242 {
		t.Errorf("top bar still reports the mid-turn reading %d after the turn ended", got)
	}
	if n := s.blackboard.FindingsCount(); n == 0 {
		t.Error("a finding posted while the session was backgrounded was dropped")
	}

}
