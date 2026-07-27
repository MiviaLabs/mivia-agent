package cli

import (
	"strings"
	"sync"
	"testing"
	"time"
)

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
	_, _, _, _, thinking, _, _ := b.Drain()
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
	stream, tools, done, err, _, _, _ := b.Drain()
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
	_, _, done, err, _, _, _ = b.Drain()
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
	stream, tools, _, _, _, _, _ := b.Drain()
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
	stream, tools, done, _, thinking, _, _ := b.Drain()
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
			_, _, finished, _, _, _, _ := b.Drain()
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
	if cmd != nil {
		t.Fatal("stale bridge tick must not schedule another poll")
	}
	if got.streamBuf.Len() != 0 {
		t.Fatalf("stale bridge data was applied: %q", got.streamBuf.String())
	}
}
