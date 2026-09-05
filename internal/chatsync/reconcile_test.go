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

func TestScanDanglingEvents_SubagentEndedClosesSubagent(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, eventsFileName)
	agent := &AgentOrigin{Task: "subtask-1", Name: "researcher", Depth: 1}
	writeWireEventsToFile(t, eventsPath, []WireEvent{
		{Seq: 1, Type: TypeTurnStarted, Payload: TurnStartedPayload{Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1"}}},
		{Seq: 2, Type: TypeSubagentStarted, Payload: SubagentStartedPayload{
			Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1", Agent: agent}, Name: "researcher", Task: "subtask-1",
		}},
		{Seq: 3, Type: TypeSubagentEnded, Payload: SubagentEndedPayload{
			Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1", Agent: agent}, Name: "researcher", Status: "success",
		}},
	})

	_, _, subs, _, err := scanDanglingEvents(dir)
	if err != nil {
		t.Fatalf("scanDanglingEvents: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("subs = %v, want none: TypeSubagentEnded must close the dangling subagent", subs)
	}
}

func TestScanDanglingEvents_SubagentToolEndedClosesSubagentTool(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, eventsFileName)
	agent := &AgentOrigin{Task: "subtask-1", Name: "researcher", Depth: 1}
	writeWireEventsToFile(t, eventsPath, []WireEvent{
		{Seq: 1, Type: TypeTurnStarted, Payload: TurnStartedPayload{Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1"}}},
		{Seq: 2, Type: TypeSubagentToolStarted, Payload: SubagentToolStartedPayload{
			Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1", Agent: agent}, ToolCallID: "tool-sub-1", Name: "view_file",
		}},
		{Seq: 3, Type: TypeSubagentToolEnded, Payload: SubagentToolEndedPayload{
			Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1", Agent: agent}, ToolCallID: "tool-sub-1", Name: "view_file", Status: "success",
		}},
	})

	_, _, _, subTools, err := scanDanglingEvents(dir)
	if err != nil {
		t.Fatalf("scanDanglingEvents: %v", err)
	}
	if len(subTools) != 0 {
		t.Fatalf("subTools = %v, want none: TypeSubagentToolEnded must close the dangling subagent tool", subTools)
	}
}

func TestScanDanglingEvents_DirIsActuallyAFile(t *testing.T) {
	// eventsPath = filepath.Join(dir, eventsFileName): if dir is itself a
	// regular file, opening that path fails with ENOTDIR, not ENOENT - the
	// one os.Open error scanDanglingEvents must propagate rather than treat
	// as "no events file yet".
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := scanDanglingEvents(notADir); err == nil {
		t.Fatal("scanDanglingEvents did not propagate a non-ENOENT os.Open error")
	}
}

func TestScanDanglingEvents_SkipsBlankLinesAndRejectsMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, eventsFileName)
	data, err := json.Marshal(WireEvent{
		Seq: 1, Type: TypeTurnStarted,
		Payload: TurnStartedPayload{Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A blank line between two entries must be skipped, not misread as a
	// malformed record.
	content := string(data) + "\n\n" + string(data) + "\n"
	if err := os.WriteFile(eventsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := scanDanglingEvents(dir); err != nil {
		t.Fatalf("scanDanglingEvents rejected a file with a blank line: %v", err)
	}

	malformed := filepath.Join(t.TempDir(), eventsFileName)
	if err := os.WriteFile(malformed, []byte("not json at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := scanDanglingEvents(filepath.Dir(malformed)); err == nil {
		t.Fatal("scanDanglingEvents accepted a malformed JSON line")
	}
}

func TestScanDanglingEvents_LineExceedsScannerBuffer(t *testing.T) {
	dir := t.TempDir()
	eventsPath := filepath.Join(dir, eventsFileName)
	// One line over the 1MB scanner.Buffer cap, no newline: bufio.Scanner
	// reports ErrTooLong via scanner.Err() after Scan() returns false.
	tooLong := make([]byte, 2*1024*1024)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	if err := os.WriteFile(eventsPath, tooLong, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := scanDanglingEvents(dir); err == nil {
		t.Fatal("scanDanglingEvents did not report the scanner's line-too-long error")
	}
}

func TestReconcileDangling_ClosesDanglingSubagentsAndToolsWithFallbackTurn(t *testing.T) {
	dir := t.TempDir()
	outbox, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer outbox.Close()

	agent := &AgentOrigin{Task: "subtask-1", Name: "researcher", Depth: 1}
	// Every dangling item's own Turn is left empty, so each of the three
	// closing-event builders must fall back to openTurn ("turn-abort").
	err = outbox.Append(
		WireEvent{Seq: 1, Type: TypeTurnStarted, Payload: TurnStartedPayload{
			Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-abort"},
		}},
		WireEvent{Seq: 2, Type: TypeToolStarted, Payload: ToolStartedPayload{
			Envelope: Envelope{V: 1, At: time.Now()}, ToolCallID: "call-unfinished", Name: "run_command",
		}},
		WireEvent{Seq: 3, Type: TypeSubagentStarted, Payload: SubagentStartedPayload{
			Envelope: Envelope{V: 1, At: time.Now(), Agent: agent}, Name: "researcher", Task: "subtask-1",
		}},
		WireEvent{Seq: 4, Type: TypeSubagentToolStarted, Payload: SubagentToolStartedPayload{
			Envelope: Envelope{V: 1, At: time.Now(), Agent: agent}, ToolCallID: "tool-sub-1", Name: "view_file",
		}},
	)
	if err != nil {
		t.Fatalf("Append interrupted events: %v", err)
	}

	session := &SyncSession{
		outbox: outbox, appender: outbox,
		projector: NewProjector("chat-test", 4, ProjectorOptions{WriterID: "cli-writer"}),
		flushCh:   make(chan struct{}, 1),
	}
	if err := session.reconcileDangling(context.Background()); err != nil {
		t.Fatalf("reconcileDangling: %v", err)
	}

	unflushed, err := outbox.UnflushedEvents()
	if err != nil {
		t.Fatalf("UnflushedEvents: %v", err)
	}
	// 4 original + subagent-tool.ended, subagent.ended, tool.ended, turn.failed
	if len(unflushed) != 8 {
		t.Fatalf("len(unflushed) = %d, want 8", len(unflushed))
	}
	kinds := map[string]bool{}
	for _, ev := range unflushed[4:] {
		kinds[ev.Type] = true
		var env struct {
			Envelope
		}
		if err := json.Unmarshal(ev.Payload, &env); err != nil {
			t.Fatalf("unmarshal closing event %s: %v", ev.Type, err)
		}
		if ev.Type != TypeTurnFailed && env.Turn != "turn-abort" {
			t.Errorf("closing event %s carries turn %q, want the openTurn fallback %q", ev.Type, env.Turn, "turn-abort")
		}
	}
	for _, want := range []string{TypeSubagentToolEnded, TypeSubagentEnded, TypeToolEnded, TypeTurnFailed} {
		if !kinds[want] {
			t.Errorf("missing synthesized closing event %s among %v", want, unflushed[4:])
		}
	}
}

func TestReconcileDangling_PropagatesScanError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, eventsFileName), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outbox, err := OpenOutbox(t.TempDir(), 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer outbox.Close()
	// outbox.dir is what reconcileDangling scans; point it at the directory
	// carrying the malformed events file above instead of outbox's own.
	outbox.dir = dir

	session := &SyncSession{
		outbox: outbox, appender: outbox,
		projector: NewProjector("chat-test", 0, ProjectorOptions{WriterID: "cli-writer"}),
		flushCh:   make(chan struct{}, 1),
	}
	if err := session.reconcileDangling(context.Background()); err == nil {
		t.Fatal("reconcileDangling did not propagate scanDanglingEvents' error")
	}
}

func TestReconcileDangling_PropagatesAppendError(t *testing.T) {
	dir := t.TempDir()
	outbox, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer outbox.Close()

	err = outbox.Append(WireEvent{Seq: 1, Type: TypeToolStarted, Payload: ToolStartedPayload{
		Envelope: Envelope{V: 1, At: time.Now(), Turn: "turn-1"}, ToolCallID: "call-1", Name: "bash",
	}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	failing := &scriptedAppender{entered: make(chan struct{}, 1), real: outbox}
	failing.fail.Store(true)
	session := &SyncSession{
		outbox: outbox, appender: failing,
		projector: NewProjector("chat-test", 1, ProjectorOptions{WriterID: "cli-writer"}),
		flushCh:   make(chan struct{}, 1),
	}
	if err := session.reconcileDangling(context.Background()); err == nil {
		t.Fatal("reconcileDangling did not propagate the appender's error")
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
