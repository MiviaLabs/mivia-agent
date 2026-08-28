package subagents

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
)

// TestEmitHeartbeatEmitsTickerEvent pins the goroutine-driven heartbeat: the
// 30s ticker must emit an EventSubagentHeartbeat carrying the live step and
// tool-call counters, then stop when the context is canceled. This is the
// fallback signal that keeps the sidebar alive between progress events.
func TestEmitHeartbeatEmitsTickerEvent(t *testing.T) {
	var stepCount, toolCallCount atomic.Int64
	stepCount.Store(3)
	toolCallCount.Store(7)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var got *agent.Event
	onEvent := func(e agent.Event) {
		mu.Lock()
		if got == nil {
			copied := e
			got = &copied
		}
		mu.Unlock()
		cancel() // one tick is enough; stop the goroutine
	}

	finished := make(chan struct{})
	go func() {
		emitHeartbeat(ctx, onEvent, &stepCount, &toolCallCount)
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(45 * time.Second):
		cancel()
		t.Fatal("emitHeartbeat did not stop after cancellation and no heartbeat arrived")
	}

	if got == nil {
		t.Fatal("no heartbeat event was emitted by the ticker")
	}
	if got.Kind != agent.EventSubagentHeartbeat {
		t.Fatalf("kind = %v, want EventSubagentHeartbeat", got.Kind)
	}
	detail := got.Detail
	if !strings.Contains(detail, "steps=3") || !strings.Contains(detail, "toolcalls=7") {
		t.Fatalf("detail = %q, want the live step and tool-call counts", detail)
	}
}
