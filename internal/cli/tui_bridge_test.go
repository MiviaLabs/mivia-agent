package cli

import (
	"testing"
	"time"
)

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
	stream, tools, done, err, _ := b.Drain()
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
	_, _, done, err, _ = b.Drain()
	if !done || err != nil {
		t.Fatalf("done=%v err=%v", done, err)
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
			_, _, finished, _, _ := b.Drain()
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
