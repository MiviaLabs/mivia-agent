package cli

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// TestUIEventMsgAppliesAssistantEvent verifies that a uiEventMsg with
// KindAssistant applies content to the thinking buffer and renders.
func TestUIEventMsgAppliesAssistantEvent(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.NewEventFromAgentParts(events.KindAssistant, "", "", "", "hello from assistant", "", "")
	model, cmd := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if got.thinkingBuf.String() != "hello from assistant" {
		t.Fatalf("expected thinking 'hello from assistant', got %q", got.thinkingBuf.String())
	}
	if cmd == nil {
		t.Fatal("uiEventMsg must re-queue pollCmd")
	}
}

// TestUIEventMsgAppliesToolStartEvent verifies that a uiEventMsg with
// KindToolStart creates a tool row.
func TestUIEventMsgAppliesToolStartEvent(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.NewEventFromAgentParts(events.KindToolStart, "call-1", "read_file", "queued", "", `{"path":"a.go"}`, "")
	model, cmd := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if len(got.toolRows) != 1 {
		t.Fatalf("expected 1 tool row, got %d", len(got.toolRows))
	}
	if got.toolRows[0].Name != "read_file" {
		t.Fatalf("expected tool name 'read_file', got %q", got.toolRows[0].Name)
	}
	if got.toolRows[0].Status != "queued" {
		t.Fatalf("expected status 'queued', got %q", got.toolRows[0].Status)
	}
	if cmd == nil {
		t.Fatal("uiEventMsg must re-queue pollCmd")
	}
}

// TestUIEventMsgAppliesToolEndEvent verifies that a uiEventMsg with
// KindToolEnd marks the matching tool row as done.
func TestUIEventMsgAppliesToolEndEvent(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	// Start tool
	startEv := events.NewEventFromAgentParts(events.KindToolStart, "call-1", "read_file", "queued", "", `{}`, "")
	m.Update(uiEventMsg{event: startEv})

	// End tool
	endEv := events.NewEventFromAgentParts(events.KindToolEnd, "call-1", "read_file", "", "", "", "file content")
	model, cmd := m.Update(uiEventMsg{event: endEv})
	got := model.(*tuiModel)

	if len(got.toolRows) != 1 {
		t.Fatalf("expected 1 tool row, got %d", len(got.toolRows))
	}
	if !got.toolRows[0].Done {
		t.Fatal("expected tool row to be marked Done")
	}
	if got.toolRows[0].Result != "file content" {
		t.Fatalf("expected result 'file content', got %q", got.toolRows[0].Result)
	}
	if cmd == nil {
		t.Fatal("uiEventMsg must re-queue pollCmd")
	}
}

// TestUIEventMsgTurnEndFinishesStream verifies that KindTurnEnd calls
// finishStream, committing the stream/thinking buffers as blocks.
func TestUIEventMsgTurnEndFinishesStream(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	// User block already present.
	m.appendBlock(ChatBlock{
		TurnID: uint64(m.session.UserTurns() + 1),
		Kind:   ChatBlockUser,
		Text:   "test",
	})

	// Simulate some assistant thinking content.
	m.thinkingBuf.WriteString("result text")

	// Send TurnEnd
	ev := events.NewEventFromAgentParts(events.KindTurnEnd, "", "", "", "", "", "")
	model, cmd := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if got.waiting {
		t.Fatal("expected waiting=false after KindTurnEnd")
	}
	if got.thinkingBuf.Len() != 0 {
		t.Fatalf("expected empty thinking buffer after finish, got %q", got.thinkingBuf.String())
	}

	// Thinking buffer content should become a thinking ChatBlock.
	found := false
	for _, blk := range got.blocks {
		if blk.Kind == ChatBlockThinking {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected a thinking ChatBlock after KindTurnEnd with thinking content")
	}
	_ = cmd
}

// TestUIEventMsgStepUpdatesDetail verifies that KindStep sets stepDetail.
func TestUIEventMsgStepUpdatesDetail(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.NewEventFromAgentParts(events.KindStep, "", "", "3/5 steps", "", "", "")
	model, cmd := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if got.stepDetail != "3/5 steps" {
		t.Fatalf("expected stepDetail '3/5 steps', got %q", got.stepDetail)
	}
	if cmd == nil {
		t.Fatal("uiEventMsg must re-queue pollCmd")
	}
}

// TestUIEventMsgErrorSetsStalled verifies that KindError sets stalled warning.
func TestUIEventMsgErrorSetsStalled(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.NewEventFromAgentParts(events.KindError, "", "", "connection refused", "", "", "")
	model, cmd := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if !got.stalledWarning {
		t.Fatal("expected stalledWarning after KindError")
	}
	if cmd == nil {
		t.Fatal("uiEventMsg must re-queue pollCmd")
	}
}

// TestUIEventMsgWelcomeModeIgnores verifies that uiEventMsg is ignored
// in welcome mode (no panic, no state change).
func TestUIEventMsgWelcomeModeIgnores(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeWelcome
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.NewEventFromAgentParts(events.KindAssistant, "", "", "", "should be ignored", "", "")
	model, cmd := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if got.thinkingBuf.Len() != 0 {
		t.Fatalf("events must be ignored in welcome mode, got thinking %q", got.thinkingBuf.String())
	}
	if cmd == nil {
		t.Fatal("uiEventMsg must re-queue pollCmd even in welcome mode")
	}
}

// TestUIEventMsgNotWaitingIgnores verifies that uiEventMsg is ignored
// when not waiting (no active agent turn).
func TestUIEventMsgNotWaitingIgnores(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = false
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.NewEventFromAgentParts(events.KindToolStart, "call-1", "read_file", "queued", "", `{}`, "")
	model, cmd := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if len(got.toolRows) != 0 {
		t.Fatalf("tool events must be ignored when not waiting, got %d rows", len(got.toolRows))
	}
	if cmd == nil {
		t.Fatal("uiEventMsg must re-queue pollCmd")
	}
}
