package cli

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamBridgeRevokeStreamClearsPendingAndFlagsReset(t *testing.T) {
	b := newStreamBridge()
	if _, err := b.Write([]byte("partial answer before tools")); err != nil {
		t.Fatal(err)
	}
	// Simulate drain of partial into UI.
	d := b.Drain()
	stream := d.Stream
	reset := d.ResetStream
	if stream != "partial answer before tools" || reset {
		t.Fatalf("stream=%q reset=%v", stream, reset)
	}
	// More optimistic content lands, then revoke.
	if _, err := b.Write([]byte(" more")); err != nil {
		t.Fatal(err)
	}
	revoked := b.RevokeStream()
	if !strings.Contains(revoked, "more") {
		t.Fatalf("revoked=%q", revoked)
	}
	d = b.Drain()
	if d.Stream != "" {
		t.Fatalf("pending should be empty after revoke, got %q", d.Stream)
	}
	if !d.ResetStream {
		t.Fatal("expected resetStream on drain after RevokeStream")
	}
	// Speech is re-emitted as Interim via EventAssistant, not thinking.
	if d.Thinking != "" {
		t.Fatalf("revoke must not dump speech into thinking, got %q", d.Thinking)
	}
	if d.Interim != "" {
		t.Fatalf("RevokeStream alone does not set Interim (agent PushInterim does), got %q", d.Interim)
	}
}

func TestUpdateFromDrainResetStreamClearsStreamBuf(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.streamBuf.WriteString("optimistic")
	m.updateFromDrain(bridgeDrain{ResetStream: true})
	if m.streamBuf.Len() != 0 {
		t.Fatalf("streamBuf should clear on resetStream, got %q", m.streamBuf.String())
	}
}

func TestInterimAssistantBecomesChatBubble(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.appendBlock(ChatBlock{Kind: ChatBlockUser, Text: "find bugs"})

	// Simulate content-then-tools: stream, revoke, interim speech, tools.
	_, _ = m.bridge.Write([]byte("I'll search the codebase first."))
	_ = m.bridge.RevokeStream()
	m.bridge.PushInterim("I'll search the codebase first.")
	m.bridge.PushToolWithID(true, "c1", "grep", `{"pattern":"bug"}`)
	m.bridge.PushToolWithID(false, "c1", "grep", "matches")

	d := m.bridge.Drain()
	m.updateFromDrain(d)

	// Interim speech is a durable assistant bubble.
	foundSpeech := false
	for _, b := range m.blocks {
		if b.Kind == ChatBlockAssistant && strings.Contains(b.Text, "I'll search") {
			foundSpeech = true
		}
	}
	if !foundSpeech {
		t.Fatalf("expected intermediate assistant bubble, blocks=%+v", m.blocks)
	}
	// Tool committed to history.
	if !hasToolBlock(m.blocks, "grep") {
		t.Fatal("expected grep tool block in history")
	}
	if m.streamBuf.Len() != 0 {
		t.Fatalf("streamBuf should be clear after revoke+drain, got %q", m.streamBuf.String())
	}
}

func TestStreamBridgeQueuedRunningDoesNotDoubleCountActiveTools(t *testing.T) {
	b := newStreamBridge()
	// Simulate agent loop: Start queued, then Start running, then End.
	b.PushToolWithID(true, "call-1", "read_file", `{"path":"a"}`)
	b.PushToolWithID(true, "call-1", "read_file", "running")
	if got := b.ActiveTools(); got != 1 {
		t.Fatalf("after queued+running activeTools=%d want 1", got)
	}
	b.PushToolWithID(false, "call-1", "read_file", "content")
	if got := b.ActiveTools(); got != 0 {
		t.Fatalf("after end activeTools=%d want 0", got)
	}
	// Thinking must be suppressed when no tools open.
	b.PushThinking("should-drop")
	d := b.Drain()
	thinking := d.Thinking
	if thinking != "" {
		t.Fatalf("thinking leaked after tools closed: %q", thinking)
	}
}

func TestStreamBridgeCoalesceAndFinish(t *testing.T) {
	b := newStreamBridge()
	if _, err := b.Write([]byte("hel")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("lo")); err != nil {
		t.Fatal(err)
	}
	b.PushTool(true, "write_file", `{"path":"a"}`)
	select {
	case <-b.notify:
	case <-time.After(time.Second):
		t.Fatal("expected notify")
	}
	d := b.Drain()
	stream := d.Stream
	tools := d.Tools
	done := d.Done
	err := d.DoneErr
	if stream != "hello" {
		t.Fatalf("stream=%q", stream)
	}
	if len(tools) != 1 || tools[0].Name != "write_file" || !tools[0].Start {
		t.Fatalf("tools=%+v", tools)
	}
	if done {
		t.Fatal("not done yet")
	}
	b.Finish(nil)
	d = b.Drain()
	done = d.Done
	err = d.DoneErr
	if !done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
	}
}

func TestStreamBridgeConcurrentProducersAreBounded(t *testing.T) {
	b := newStreamBridge()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				_, _ = b.Write([]byte("x"))
				b.PushTool(true, "read_file", strings.Repeat("s", 1000))
				b.PushTool(false, "read_file", "done")
			}
		}()
	}
	wg.Wait()
	d := b.Drain()
	stream := d.Stream
	tools := d.Tools
	if len(stream) > 512*1024 {
		t.Fatalf("stream exceeded cap: %d", len(stream))
	}
	if len(tools) > 500 {
		t.Fatalf("tool events exceeded cap: %d", len(tools))
	}
}

func TestStreamBridgeCloseDropsStaleEventsAndIsIdempotent(t *testing.T) {
	b := newStreamBridge()
	b.Close()
	b.Close()
	_, _ = b.Write([]byte("stale"))
	b.PushTool(true, "secret_tool", "token=should-not-appear")
	b.PushThinking("stale thinking")
	b.Finish(nil)
	d := b.Drain()
	stream := d.Stream
	tools := d.Tools
	done := d.Done
	thinking := d.Thinking
	if stream != "" || len(tools) != 0 || thinking != "" {
		t.Fatalf("closed bridge retained stale data: stream=%q tools=%d thinking=%q", stream, len(tools), thinking)
	}
	if !done {
		t.Fatal("expected finish signal to remain observable")
	}
}

func TestStreamBridgeNoHangOnBurst(t *testing.T) {
	b := newStreamBridge()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			_, _ = b.Write([]byte("x"))
		}
		b.Finish(nil)
		close(done)
	}()
	go func() {
		for {
			d := b.Drain()
			finished := d.Done
			if finished {
				return
			}
			// Use channel wait via notify when possible
			select {
			case <-b.notify:
			case <-time.After(time.Millisecond):
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("write burst hung")
	}
}

func TestTUIIgnoresStaleBridgeTick(t *testing.T) {
	oldBridge := newStreamBridge()
	currentBridge := newStreamBridge()
	m := &tuiModel{bridge: currentBridge, waiting: true}
	_, _ = oldBridge.Write([]byte("stale"))

	model, cmd := m.Update(tuiTickMsg{bridge: oldBridge})
	got := model.(*tuiModel)
	// Phase 1: pollCmd is always re-queued (chain stays alive) even for stale ticks.
	// The pollCmd reads m.bridge live, so it will use the current bridge.
	if cmd == nil {
		t.Fatal("stale bridge tick must still re-queue pollCmd (chain stays alive)")
	}
	if got.streamBuf.Len() != 0 {
		t.Fatalf("stale bridge data was applied: %q", got.streamBuf.String())
	}
}

func TestStreamBridgeStaleEventFence(t *testing.T) {
	b := newStreamBridge()

	// Fence for turn 1 — clears done and sets turnID.
	b.FenceTurn(1)

	// Push events for turn 1 — should work.
	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	b.PushTool(true, "tool1", "detail1")
	b.PushThinking("thinking text")

	// Drain to get everything.
	d := b.Drain()
	stream := d.Stream
	tools := d.Tools
	done := d.Done
	thinking := d.Thinking
	if stream != "hello" {
		t.Fatalf("stream=%q want 'hello'", stream)
	}
	if len(tools) != 1 || tools[0].Name != "tool1" {
		t.Fatalf("tools=%+v", tools)
	}
	if thinking != "thinking text\n" {
		t.Fatalf("thinking=%q want 'thinking text\\n'", thinking)
	}
	if done {
		t.Fatal("should not be done after FenceTurn")
	}

	// Finish the turn.
	b.Finish(nil)

	// Now try to push more — should be dropped (fenced: done && turnID > 0).
	_, _ = b.Write([]byte("stale"))
	b.PushTool(true, "tool2", "should-not-appear")
	b.PushThinking("stale thinking")

	// Drain: the Finish signal must still be visible.
	d = b.Drain()
	stream = d.Stream
	tools = d.Tools
	done = d.Done
	thinking = d.Thinking
	if stream != "" {
		t.Fatalf("stale stream leaked: %q", stream)
	}
	if len(tools) != 0 {
		t.Fatalf("stale tools leaked: %+v", tools)
	}
	if thinking != "" {
		t.Fatalf("stale thinking leaked: %q", thinking)
	}
	if !done {
		t.Fatal("expected done=true after Finish")
	}

	// After drain, turnID is cleared. Fence for turn 2 — clears done.
	b.FenceTurn(2)

	_, _ = b.Write([]byte("world"))
	b.PushTool(true, "tool3", "detail3")

	d = b.Drain()
	stream = d.Stream
	tools = d.Tools
	done = d.Done
	if stream != "world" {
		t.Fatalf("stream=%q want 'world'", stream)
	}
	if len(tools) != 1 || tools[0].Name != "tool3" {
		t.Fatalf("tools=%+v", tools)
	}
	if done {
		t.Fatal("should not be done after FenceTurn(2)")
	}
}
