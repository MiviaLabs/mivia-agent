package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

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

// TestUIEventMsgAssistantIgnoredOnBus verifies content is bridge-owned
// (bus KindAssistant must not write streamBuf — avoids double apply).
func TestUIEventMsgAssistantIgnoredOnBus(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.NewEventFromAgentParts(events.KindAssistant, "", "", "", "hello from assistant", "", "")
	model, _ := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if got.streamBuf.Len() != 0 {
		t.Fatalf("bus KindAssistant must not fill streamBuf (bridge owns content), got %q", got.streamBuf.String())
	}
}

// TestUIEventMsgTurnEndBackupFinish verifies TurnEnd can finish if still waiting.
func TestUIEventMsgTurnEndBackupFinish(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.activeTurnID = "1"
	m.streamBuf.WriteString("from bridge earlier")
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.Event{
		Kind:      events.KindTurnEnd,
		Timestamp: time.Now(),
		TurnID:    "1",
	}
	model, _ := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if got.waiting {
		t.Fatal("expected waiting=false after KindTurnEnd backup finish")
	}
	found := false
	for _, blk := range got.blocks {
		if blk.Kind == ChatBlockAssistant && blk.Text == "from bridge earlier" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected assistant block committed on TurnEnd backup")
	}
}

// TestUIEventMsgTurnEndWithErrorShowsErrorFooter verifies error detail maps to finishStream.
func TestUIEventMsgTurnEndWithErrorShowsErrorFooter(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.activeTurnID = "1"
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.Event{
		Kind:      events.KindTurnEnd,
		Timestamp: time.Now(),
		TurnID:    "1",
		Detail:    "connection refused",
		Err:       errors.New("connection refused"),
	}
	model, _ := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	found := false
	for _, blk := range got.blocks {
		if blk.Kind == ChatBlockDivider && strings.Contains(blk.Text, "error:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected error divider after KindTurnEnd with Err")
	}
}

// TestUIEventMsgTurnEndCancelShowsCancelFooter verifies context.Canceled identity.
func TestUIEventMsgTurnEndCancelShowsCancelFooter(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.activeTurnID = "1"
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.Event{
		Kind:      events.KindTurnEnd,
		Timestamp: time.Now(),
		TurnID:    "1",
		Detail:    context.Canceled.Error(),
		Err:       context.Canceled,
	}
	model, _ := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	found := false
	for _, blk := range got.blocks {
		if blk.Kind == ChatBlockDivider && strings.Contains(blk.Text, "cancelled") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected cancelled divider after KindTurnEnd with context.Canceled")
	}
}

// TestUIEventMsgStaleTurnEndIgnored verifies force-send fencing.
func TestUIEventMsgStaleTurnEndIgnored(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.activeTurnID = "2"
	m.streamBuf.WriteString("turn 2 content")
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.Event{
		Kind:      events.KindTurnEnd,
		Timestamp: time.Now(),
		TurnID:    "1",
	}
	model, _ := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if !got.waiting {
		t.Fatal("stale TurnEnd must not finish the active turn")
	}
	if got.streamBuf.String() != "turn 2 content" {
		t.Fatalf("stale TurnEnd must not clear streamBuf, got %q", got.streamBuf.String())
	}
}

// TestUIEventMsgWelcomeModeIgnores verifies that uiEventMsg is ignored
// in welcome mode (no panic, no state change).
func TestUIEventMsgWelcomeModeIgnores(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeWelcome
	m.uiAdapter = NewUIAdapter(events.New(), nil)

	ev := events.NewEventFromAgentParts(events.KindStep, "", "", "should be ignored", "", "", "")
	model, cmd := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if got.stepDetail != "" {
		t.Fatalf("events must be ignored in welcome mode, got step %q", got.stepDetail)
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

	ev := events.NewEventFromAgentParts(events.KindError, "", "", "x", "", "", "")
	model, cmd := m.Update(uiEventMsg{event: ev})
	got := model.(*tuiModel)

	if got.stalledWarning {
		t.Fatal("error events must be ignored when not waiting")
	}
	if cmd == nil {
		t.Fatal("uiEventMsg must re-queue pollCmd")
	}
}

// TestFinishStreamIdempotent verifies a second finish does not duplicate blocks.
func TestFinishStreamIdempotent(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.streamBuf.WriteString("once")

	_ = m.finishStream(nil)
	n := len(m.blocks)
	_ = m.finishStream(nil)
	if len(m.blocks) != n {
		t.Fatalf("second finishStream must be no-op: before=%d after=%d", n, len(m.blocks))
	}
}

// TestBridgePathAssistantToolsAndFinish is the production regression:
// FinalWriter stream + OnEvent tools + Finish must produce assistant + tool + done.
func TestBridgePathAssistantToolsAndFinish(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	// uiAdapter present must NOT block bridge drain (the production bug).
	m.uiAdapter = NewUIAdapter(events.New(), m.bridge)

	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "what is next"})

	_, _ = m.bridge.Write([]byte("Ship the bridge drain fix."))
	m.bridge.PushToolWithID(true, "call-1", "read_file", `{"path":"a.go"}`)
	m.bridge.PushToolWithID(false, "call-1", "read_file", "ok")
	m.bridge.Finish(nil)

	model, cmd := m.Update(tuiTickMsg{bridge: m.bridge})
	got := model.(*tuiModel)
	if cmd == nil {
		t.Fatal("tuiTickMsg must re-queue pollCmd")
	}
	if got.waiting {
		t.Fatal("expected waiting=false after bridge Finish drain")
	}

	var asst, tool, done bool
	for _, blk := range got.blocks {
		switch blk.Kind {
		case ChatBlockAssistant:
			if blk.Text == "Ship the bridge drain fix." {
				asst = true
			}
		case ChatBlockTool:
			if blk.ToolName == "read_file" {
				tool = true
			}
		case ChatBlockDivider:
			if strings.Contains(blk.Text, "done") {
				done = true
			}
		}
	}
	if !asst {
		t.Fatal("expected assistant ChatBlock from bridge FinalWriter")
	}
	if !tool {
		t.Fatal("expected tool ChatBlock from bridge OnEvent tools")
	}
	if !done {
		t.Fatal("expected done divider after finish")
	}
}

// TestPollCmdUsesBridgeNotAdapterOnly verifies production poll path.
func TestPollCmdUsesBridgeNotAdapterOnly(t *testing.T) {
	m := newSmokeModel(t)
	m.uiAdapter = NewUIAdapter(events.New(), m.bridge)
	cmd := m.pollCmd()
	if cmd == nil {
		t.Fatal("pollCmd must return a command")
	}
	// Execute once with empty bridge — should yield tuiTickMsg, not hang on adapter.
	done := make(chan teaMsgOrErr, 1)
	go func() {
		msg := cmd()
		done <- teaMsgOrErr{msg: msg}
	}()
	select {
	case r := <-done:
		if _, ok := r.msg.(tuiTickMsg); !ok {
			t.Fatalf("expected tuiTickMsg from pollCmd, got %T", r.msg)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pollCmd timed out — still blocking on adapter channel?")
	}
}

type teaMsgOrErr struct {
	msg interface{}
}
