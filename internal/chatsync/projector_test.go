package chatsync

import (
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func TestProjectorSessionFilter(t *testing.T) {
	p := NewProjector("sess-123", 0, ProjectorOptions{})

	// 1. Matching session ID: projected
	evMatch := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-123",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
		Detail:    "hello",
	}
	we := p.Project(evMatch)
	if len(we) != 1 {
		t.Fatalf("matching session event produced %d wire events, want 1", len(we))
	}
	if we[0].Seq != 1 {
		t.Errorf("seq = %d, want 1", we[0].Seq)
	}

	// 2. Empty session ID: rejected (must not match empty session id or consume seq)
	evEmpty := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
		Detail:    "hello",
	}
	weEmpty := p.Project(evEmpty)
	if len(weEmpty) != 0 {
		t.Fatalf("empty session event produced %d wire events, want 0", len(weEmpty))
	}

	// 3. Different session ID: rejected
	evOther := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-456",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
		Detail:    "hello",
	}
	weOther := p.Project(evOther)
	if len(weOther) != 0 {
		t.Fatalf("different session event produced %d wire events, want 0", len(weOther))
	}

	// 4. Subsequent matching event gets seq 2 (no seq wasted on filtered events)
	evNext := events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: "sess-123",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
		Detail:    "completed",
	}
	weNext := p.Project(evNext)
	if len(weNext) != 1 {
		t.Fatalf("next matching event produced %d wire events, want 1", len(weNext))
	}
	if weNext[0].Seq != 2 {
		t.Errorf("seq = %d, want 2", weNext[0].Seq)
	}
}

func TestProjectorEmptySessionIDCannotMatchEmptyFilter(t *testing.T) {
	// If projector was created with empty session id (defensive case), empty events must still be rejected.
	p := NewProjector("", 0, ProjectorOptions{})
	ev := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "",
		TurnID:    "turn:1",
		Timestamp: time.Now(),
		Detail:    "hello",
	}
	we := p.Project(ev)
	if len(we) != 0 {
		t.Fatalf("empty session event matched empty projector session, want 0 events")
	}
}

func TestProjectorUnrelayedKindsProduceZeroEvents(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})
	unrelayed := []events.Kind{
		events.KindUIResize,
		events.KindConfigChange,
		events.KindTokenUsage,
		events.KindCacheUsage,
	}

	for _, k := range unrelayed {
		ev := events.Event{
			Kind:      k,
			SessionID: "sess-1",
			TurnID:    "turn:1",
			Timestamp: time.Now(),
		}
		we := p.Project(ev)
		if len(we) != 0 {
			t.Errorf("unrelayed kind %q produced %d wire events, want 0", k, len(we))
		}
	}
	if p.LastSeq() != 0 {
		t.Errorf("lastSeq = %d after unrelayed kinds, want 0", p.LastSeq())
	}
}

func TestProjectorEmptyTurnIDGetsSyntheticTurn(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})
	ev := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "",
		Timestamp: time.Now(),
		Detail:    "unbound start",
	}
	we := p.Project(ev)
	if len(we) != 1 {
		t.Fatalf("got %d wire events, want 1", len(we))
	}
	payload, ok := we[0].Payload.(*TurnStartedPayload)
	if !ok {
		t.Fatalf("payload type = %T, want *TurnStartedPayload", we[0].Payload)
	}
	if payload.Turn == "" {
		t.Error("Turn in envelope is empty, want synthetic turn")
	}
	if !payload.Synthetic {
		t.Error("Synthetic flag is false, want true for empty TurnID")
	}
}

func TestProjectorDerivedBlockKeys(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// Tool start/end -> tool_call_id
	toolStart := events.Event{
		Kind:       events.KindToolStart,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: "call_abc123",
		Name:       "run_command",
		Input:      `{"cmd":"ls"}`,
		Timestamp:  time.Now(),
	}
	weTool := p.Project(toolStart)
	if len(weTool) != 1 {
		t.Fatalf("tool start produced %d wire events, want 1", len(weTool))
	}
	pTool := weTool[0].Payload.(*ToolStartedPayload)
	if pTool.Block != "call_abc123" {
		t.Errorf("tool block key = %q, want call_abc123", pTool.Block)
	}

	// Thinking -> turn:thinking:<step>. The stream id is the stable part; the
	// suffix is the step within the turn, which is what lets a consumer tell
	// one utterance from the next (see proseBlock).
	thinking := events.Event{
		Kind:      events.KindThinking,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Content:   "deep thought",
		Timestamp: time.Now(),
	}
	weThink := p.Project(thinking)
	if len(weThink) != 1 {
		t.Fatalf("thinking produced %d wire events, want 1", len(weThink))
	}
	pThink := weThink[0].Payload.(*ThinkingDeltaPayload)
	if pThink.Block != "turn:1:thinking:0" {
		t.Errorf("thinking block key = %q, want turn:1:thinking:0", pThink.Block)
	}

	// Assistant -> turn:assistant:<step>
	assistant := events.Event{
		Kind:      events.KindAssistant,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Content:   "final answer",
		Timestamp: time.Now(),
	}
	weAssist := p.Project(assistant)
	if len(weAssist) != 1 {
		t.Fatalf("assistant produced %d wire events, want 1", len(weAssist))
	}
	pAssist := weAssist[0].Payload.(*AssistantMessagePayload)
	if pAssist.Block != "turn:1:assistant:0" {
		t.Errorf("assistant block key = %q, want turn:1:assistant:0", pAssist.Block)
	}
}

func TestProjectorAgentOriginAttribution(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// Root agent event (no AgentTask / AgentName / AgentDepth)
	rootEv := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "root task",
		Timestamp: time.Now(),
	}
	weRoot := p.Project(rootEv)
	if len(weRoot) != 1 {
		t.Fatalf("root event produced %d wire events, want 1", len(weRoot))
	}
	pRoot := weRoot[0].Payload.(*TurnStartedPayload)
	if pRoot.Agent != nil {
		t.Errorf("root event has agent origin %+v, want nil", pRoot.Agent)
	}

	// Subagent event
	subEv := events.Event{
		Kind:       events.KindSubagentStart,
		SessionID:  "sess-1",
		TurnID:     "turn:1",
		ToolCallID: "sub_1",
		Name:       "task_runner",
		AgentTask:  "research codebase",
		AgentName:  "researcher",
		AgentDepth: 2,
		Timestamp:  time.Now(),
	}
	weSub := p.Project(subEv)
	if len(weSub) != 1 {
		t.Fatalf("subagent event produced %d wire events, want 1", len(weSub))
	}
	pSub := weSub[0].Payload.(*SubagentToolStartedPayload)
	if pSub.Agent == nil {
		t.Fatal("subagent event has nil agent origin")
	}
	if pSub.Agent.Task != "research codebase" || pSub.Agent.Name != "researcher" || pSub.Agent.Depth != 2 {
		t.Errorf("subagent origin = %+v, want task=research codebase, name=researcher, depth=2", pSub.Agent)
	}
}

func TestProjectorErrorEventMessageClassification(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	// Start turn so terminal is accepted
	p.Project(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "task",
		Timestamp: time.Now(),
	})

	// Event with Err
	errEv := events.Event{
		Kind:      events.KindError,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Err:       errors.New("something went wrong"),
		Timestamp: time.Now(),
	}
	weErr := p.Project(errEv)
	if len(weErr) != 1 {
		t.Fatalf("error event produced %d wire events, want 1", len(weErr))
	}
	pErr := weErr[0].Payload.(*TurnFailedPayload)
	if pErr.Message != "chat turn failed" {
		t.Errorf("error message = %q, want 'chat turn failed'", pErr.Message)
	}
}

func TestProjectorSyntheticTurnLifecycle(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{})

	weStart := p.Project(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "",
		Detail:    "prompt",
		Timestamp: time.Now(),
	})
	if len(weStart) != 1 {
		t.Fatalf("weStart len = %d, want 1", len(weStart))
	}
	pStart := weStart[0].Payload.(*TurnStartedPayload)
	if pStart.Turn != "synthetic:1" {
		t.Errorf("turn ID = %q, want synthetic:1", pStart.Turn)
	}

	weEnd := p.Project(events.Event{
		Kind:      events.KindTurnEnd,
		SessionID: "sess-1",
		TurnID:    "",
		Detail:    "done",
		Timestamp: time.Now(),
	})
	if len(weEnd) != 1 {
		t.Fatalf("weEnd len = %d, want 1 (turn.ended must not be dropped)", len(weEnd))
	}
	pEnd := weEnd[0].Payload.(*TurnEndedPayload)
	if pEnd.Turn != "synthetic:1" {
		t.Errorf("end turn ID = %q, want synthetic:1", pEnd.Turn)
	}
}

func TestProjectorThinkingDelta_SequentialIndex(t *testing.T) {
	p := NewProjector("sess-1", 0, ProjectorOptions{IncludeThinking: true})

	_ = p.Project(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Detail:    "task",
		Timestamp: time.Now(),
	})

	we1 := p.Project(events.Event{
		Kind:      events.KindThinking,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Content:   "chunk 1",
		Timestamp: time.Now(),
	})
	we2 := p.Project(events.Event{
		Kind:      events.KindThinking,
		SessionID: "sess-1",
		TurnID:    "turn:1",
		Content:   "chunk 2",
		Timestamp: time.Now(),
	})

	if len(we1) != 1 || len(we2) != 1 {
		t.Fatalf("we1 len = %d, we2 len = %d", len(we1), len(we2))
	}
	p1 := we1[0].Payload.(*ThinkingDeltaPayload)
	p2 := we2[0].Payload.(*ThinkingDeltaPayload)
	if p1.Index != 0 {
		t.Errorf("p1.Index = %d, want 0", p1.Index)
	}
	if p2.Index != 1 {
		t.Errorf("p2.Index = %d, want 1", p2.Index)
	}
}

func TestProjector_CrossSessionDropsIgnored(t *testing.T) {
	p := NewProjector("sess-target", 0, ProjectorOptions{})

	// Event for a different session arrives with drop counter = 5
	otherEv := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-other",
		TurnID:    "turn:1",
		Detail:    "foreign message",
		Timestamp: time.Now(),
	}

	weOther := p.ProjectWithDrops(otherEv, 5)
	if len(weOther) != 0 {
		t.Fatalf("expected 0 wire events for other session, got %d: %+v", len(weOther), weOther)
	}

	// Subsequent matching event for target session with same drop counter = 5
	// must emit the sync.dropped event because it wasn't consumed by other session!
	targetEv := events.Event{
		Kind:      events.KindTurnStart,
		SessionID: "sess-target",
		TurnID:    "turn:1",
		Detail:    "target message",
		Timestamp: time.Now(),
	}

	weTarget := p.ProjectWithDrops(targetEv, 5)
	if len(weTarget) != 2 {
		t.Fatalf("expected 2 wire events (sync.dropped + turn.started), got %d: %+v", len(weTarget), weTarget)
	}
	if weTarget[0].Type != TypeSyncDropped {
		t.Errorf("weTarget[0].Type = %q, want %q", weTarget[0].Type, TypeSyncDropped)
	}
	if weTarget[1].Type != TypeTurnStarted {
		t.Errorf("weTarget[1].Type = %q, want %q", weTarget[1].Type, TypeTurnStarted)
	}
}
