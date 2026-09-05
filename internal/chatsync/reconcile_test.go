package chatsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func TestScanDanglingEvents_Empty(t *testing.T) {
	dir := t.TempDir()
	openTurn, tools, subs, subTools, err := scanDanglingEvents(dir)
	if err != nil {
		t.Fatalf("scanDanglingEvents: %v", err)
	}
	if openTurn != "" || len(tools) != 0 || len(subs) != 0 || len(subTools) != 0 {
		t.Fatalf("expected empty scan results, got turn=%q, tools=%d, subs=%d, subTools=%d",
			openTurn, len(tools), len(subs), len(subTools))
	}
}

func TestScanDanglingEvents_CompletedTurn(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, eventsFileName)

	wireEvents := []WireEvent{
		{
			Seq:  1,
			Type: TypeTurnStarted,
			Payload: TurnStartedPayload{
				Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1"},
			},
		},
		{
			Seq:  2,
			Type: TypeToolStarted,
			Payload: ToolStartedPayload{
				Envelope:   Envelope{V: 1, At: time.Now(), Turn: "turn-1"},
				ToolCallID: "call-1",
				Name:       "bash",
			},
		},
		{
			Seq:  3,
			Type: TypeToolEnded,
			Payload: ToolEndedPayload{
				Envelope:   Envelope{V: 1, At: time.Now(), Turn: "turn-1"},
				ToolCallID: "call-1",
				Name:       "bash",
				Status:     "success",
			},
		},
		{
			Seq:  4,
			Type: TypeTurnEnded,
			Payload: TurnEndedPayload{
				Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1"},
			},
		},
	}

	writeWireEventsToFile(t, eventsPath, wireEvents)

	openTurn, tools, subs, subTools, err := scanDanglingEvents(dir)
	if err != nil {
		t.Fatalf("scanDanglingEvents: %v", err)
	}
	if openTurn != "" || len(tools) != 0 || len(subs) != 0 || len(subTools) != 0 {
		t.Fatalf("expected no dangling events for completed turn, got turn=%q, tools=%v, subs=%v, subTools=%v",
			openTurn, tools, subs, subTools)
	}
}

func TestScanDanglingEvents_InterruptedTurn(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, eventsFileName)

	wireEvents := []WireEvent{
		{
			Seq:  1,
			Type: TypeTurnStarted,
			Payload: TurnStartedPayload{
				Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1"},
			},
		},
		{
			Seq:  2,
			Type: TypeToolStarted,
			Payload: ToolStartedPayload{
				Envelope:   Envelope{V: 1, At: time.Now(), Turn: "turn-1"},
				ToolCallID: "tool-root-1",
				Name:       "search_web",
			},
		},
		{
			Seq:  3,
			Type: TypeSubagentStarted,
			Payload: SubagentStartedPayload{
				Envelope: Envelope{
					V:     1,
					At:    time.Now(),
					Turn:  "turn-1",
					Agent: &AgentOrigin{Task: "subtask-1", Name: "researcher", Depth: 1},
				},
				Name: "researcher",
				Task: "subtask-1",
			},
		},
		{
			Seq:  4,
			Type: TypeSubagentToolStarted,
			Payload: SubagentToolStartedPayload{
				Envelope: Envelope{
					V:     1,
					At:    time.Now(),
					Turn:  "turn-1",
					Agent: &AgentOrigin{Task: "subtask-1", Name: "researcher", Depth: 1},
				},
				ToolCallID: "tool-sub-1",
				Name:       "view_file",
			},
		},
	}

	writeWireEventsToFile(t, eventsPath, wireEvents)

	openTurn, tools, subs, subTools, err := scanDanglingEvents(dir)
	if err != nil {
		t.Fatalf("scanDanglingEvents: %v", err)
	}
	if openTurn != "turn-1" {
		t.Errorf("openTurn = %q, want 'turn-1'", openTurn)
	}
	if len(tools) != 1 || tools[0].toolCallID != "tool-root-1" {
		t.Errorf("tools = %v, want tool-root-1", tools)
	}
	if len(subs) != 1 || subs[0].task != "subtask-1" {
		t.Errorf("subs = %v, want subtask-1", subs)
	}
	if len(subTools) != 1 || subTools[0].toolCallID != "tool-sub-1" {
		t.Errorf("subTools = %v, want tool-sub-1", subTools)
	}
}

func TestReconcileDangling_SynthesizesClosingEventsAndAdvancesSeq(t *testing.T) {
	dir := t.TempDir()
	outbox, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer outbox.Close()

	// Append an interrupted turn with a tool
	err = outbox.Append(
		WireEvent{
			Seq:  1,
			Type: TypeTurnStarted,
			Payload: TurnStartedPayload{
				Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-abort"},
			},
		},
		WireEvent{
			Seq:  2,
			Type: TypeToolStarted,
			Payload: ToolStartedPayload{
				Envelope:   Envelope{V: 1, At: time.Now(), Turn: "turn-abort"},
				ToolCallID: "call-unfinished",
				Name:       "run_command",
			},
		},
	)
	if err != nil {
		t.Fatalf("Append interrupted events: %v", err)
	}

	session := &SyncSession{
		outbox:    outbox,
		appender:  outbox,
		projector: NewProjector("chat-test", 2, ProjectorOptions{WriterID: "cli-writer"}),
		flushCh:   make(chan struct{}, 1),
	}

	ctx := context.Background()
	if err := session.reconcileDangling(ctx); err != nil {
		t.Fatalf("reconcileDangling: %v", err)
	}

	// Projector seq should have advanced for the 2 synthesized closing events
	// (tool.ended and turn.failed)
	lastSeq := session.projector.LastSeq()
	if lastSeq != 4 {
		t.Fatalf("projector.LastSeq() = %d, want 4", lastSeq)
	}

	// Read unflushed events and verify synthesized records
	unflushed, err := outbox.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	if len(unflushed) != 4 {
		t.Fatalf("len(unflushed) = %d, want 4", len(unflushed))
	}

	// Event 3: tool.ended with status "interrupted"
	if unflushed[2].Seq != 3 || unflushed[2].Type != TypeToolEnded {
		t.Errorf("event 3 = seq %d, type %s; want seq 3, type %s",
			unflushed[2].Seq, unflushed[2].Type, TypeToolEnded)
	}
	var toolEnded ToolEndedPayload
	if err := json.Unmarshal(unflushed[2].Payload, &toolEnded); err != nil {
		t.Fatalf("unmarshal toolEnded: %v", err)
	}
	if toolEnded.ToolCallID != "call-unfinished" || toolEnded.Status != "interrupted" {
		t.Errorf("toolEnded = %+v, want toolCallID 'call-unfinished', status 'interrupted'", toolEnded)
	}

	// Event 4: turn.failed
	if unflushed[3].Seq != 4 || unflushed[3].Type != TypeTurnFailed {
		t.Errorf("event 4 = seq %d, type %s; want seq 4, type %s",
			unflushed[3].Seq, unflushed[3].Type, TypeTurnFailed)
	}
}

func TestSyncSession_HandleEventStatusTransitions(t *testing.T) {
	dir := t.TempDir()
	outbox, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer outbox.Close()

	s := newSyncSession("chat-test", nil, outbox, SessionOptions{}, CreateSessionParams{})
	if s.currentStatus != "waiting_input" {
		t.Fatalf("initial status = %q, want 'waiting_input'", s.currentStatus)
	}

	ctx := context.Background()

	// TurnStart transitions to running
	s.HandleEvent(ctx, events.Event{Kind: events.KindTurnStart})
	s.statusMu.Lock()
	status1 := s.currentStatus
	s.statusMu.Unlock()
	if status1 != "running" {
		t.Errorf("after KindTurnStart, status = %q, want 'running'", status1)
	}

	// TurnEnd transitions to waiting_input
	s.HandleEvent(ctx, events.Event{Kind: events.KindTurnEnd})
	s.statusMu.Lock()
	status2 := s.currentStatus
	s.statusMu.Unlock()
	if status2 != "waiting_input" {
		t.Errorf("after KindTurnEnd, status = %q, want 'waiting_input'", status2)
	}

	// TurnStart then Error transitions back to waiting_input
	s.HandleEvent(ctx, events.Event{Kind: events.KindTurnStart})
	s.HandleEvent(ctx, events.Event{Kind: events.KindError})
	s.statusMu.Lock()
	status3 := s.currentStatus
	s.statusMu.Unlock()
	if status3 != "waiting_input" {
		t.Errorf("after KindError, status = %q, want 'waiting_input'", status3)
	}
}

func writeWireEventsToFile(t *testing.T, path string, evs []WireEvent) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	for _, ev := range evs {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal %v: %v", ev, err)
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			t.Fatalf("write %v: %v", ev, err)
		}
	}
}
