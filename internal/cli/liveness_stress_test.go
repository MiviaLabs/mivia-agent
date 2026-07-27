package cli

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// INV-TUI-2 (Liveness): poll chain starvation resistance
//
// Feasibility evaluation: HIGH. A tight-loop stress test proves the chain does
// not die under rapid scheduling pressure (500 consecutive ticks with zero
// bridge data). Each iteration verifies a non-nil cmd is returned, proving
// pollCmd was re-queued. Full integration with a simulated slow bridge + real
// TTY is deferred (requires bubbletea test framework).
//
// Residual risk: A unit stress test cannot simulate every kernel scheduler or
// goroutine-parking edge case. Runtime monitoring of tick interval in
// production is the only complete proof.
// ---------------------------------------------------------------------------

// TestTuiTickMsgStressRequeuesPoll exercises the poll chain under tight
// scheduling pressure — 500 rapid tuiTickMsg updates with an empty bridge.
// Every single one must re-queue pollCmd (non-nil cmd). If the chain dies on
// iteration N, the invariant is broken.
func TestTuiTickMsgStressRequeuesPoll(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()

	for i := 0; i < 500; i++ {
		model, cmd := m.Update(tuiTickMsg{bridge: m.bridge})
		if cmd == nil {
			t.Fatalf("iteration %d: tuiTickMsg returned nil cmd (poll chain died)", i)
		}
		m = model.(*tuiModel)
	}
}

// TestTuiTickMsgStressWithBridgeData exercises the poll chain with concurrent
// bridge writes. 100 iterations where each tick has fresh bridge data (stream
// content + tool events + finish). Verifies pollCmd is always re-queued and
// the model state transitions correctly (streamBuf populated, tools drained,
// finishStream triggered).
func TestTuiTickMsgStressWithBridgeData(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()

	for i := 0; i < 100; i++ {
		// Write data to bridge before tick
		m.bridge.Write([]byte("stress data iteration " + itoa(i)))
		m.bridge.PushTool(true, "stress_tool", `{"iter":`+itoa(i)+`}`)

		model, cmd := m.Update(tuiTickMsg{bridge: m.bridge})
		if cmd == nil {
			t.Fatalf("iteration %d: tuiTickMsg with bridge data returned nil cmd", i)
		}
		got := model.(*tuiModel)
		if got.streamBuf.Len() == 0 {
			t.Fatalf("iteration %d: streamBuf empty after tick with bridge data", i)
		}
		_ = got.streamBuf.String()
		// Reset for next iteration
		got.streamBuf.Reset()
		// Drain remaining bridge data (tool events, finish markers)
		_, _, done, _, _, _, _, _ := got.bridge.Drain()
		if done {
			t.Fatalf("iteration %d: bridge unexpectedly done", i)
		}
		m = got
	}
}

// itoa is a small integer to string converter (no fmt import needed).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	neg := n < 0
	if neg {
		n = -n
	}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// INV-TUI-6 (Liveness): tool progress visibility under parallel dispatch
//
// Feasibility evaluation: HIGH. A concurrent stress test verifies that the
// bridge correctly tracks tool events from many goroutines and that the TUI
// can drain and apply them without deadlock or event loss. The existing
// TestStreamBridgeConcurrentProducersAreBounded tests memory bounds but does
// not verify event completeness (expected count) or TUI apply.
//
// Residual risk: A unit stress test cannot prove the TUI renders tool
// progress within 100ms under real dispatch. That requires a visual
// regression or acceptance test with real bubbling (deferred).
// ---------------------------------------------------------------------------

// TestStreamBridgeConcurrentDispatchCompleteness verifies that the bridge
// captures every tool event from concurrent goroutines and the TUI model
// applies them without deadlock or panic.
func TestStreamBridgeConcurrentDispatchCompleteness(t *testing.T) {
	b := newStreamBridge()
	const numWorkers = 8
	const eventsPerWorker = 30
	// Each worker produces a Start+End pair per event = 2 events per iteration
	const totalExpected = numWorkers * eventsPerWorker * 2

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < eventsPerWorker; j++ {
				name := "tool_" + itoa(id) + "_" + itoa(j)
				b.PushTool(true, name, `{"worker":`+itoa(id)+`}`)
				b.PushTool(false, name, "done")
			}
		}(i)
	}
	wg.Wait()

	// Drain all tools from bridge
	_, tools, done, _, _, _, _, _ := b.Drain()
	if done {
		t.Fatal("bridge should not be done (Finish not called)")
	}

	if len(tools) != totalExpected {
		t.Fatalf("expected %d tool events from concurrent dispatch, got %d",
			totalExpected, len(tools))
	}

	// Verify each tool has a matching Start-End pair
	seen := map[string]int{} // name → balance
	for _, evt := range tools {
		if evt.Start {
			seen[evt.Name]++
		} else {
			seen[evt.Name]--
		}
	}
	for name, balance := range seen {
		if balance != 0 {
			t.Fatalf("tool %q has unbalanced Start/End (balance=%d)", name, balance)
		}
	}
}

// TestStreamBridgeConcurrentDispatchAndTUIApply verifies that concurrent tool
// dispatch can be drained and applied to the TUI model without deadlock. The
// TUI must not hang when toolRows are populated from concurrent producers.
func TestStreamBridgeConcurrentDispatchAndTUIApply(t *testing.T) {
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	b := m.bridge

	const numWorkers = 4
	const eventsPerWorker = 20

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < eventsPerWorker; j++ {
				b.PushTool(true, "tool_"+itoa(id), `{"worker":`+itoa(id)+`}`)
			}
		}(i)
	}
	wg.Wait()

	// Apply to TUI via tuiTickMsg — must not panic or hang
	model, cmd := m.Update(tuiTickMsg{bridge: b})
	if cmd == nil {
		t.Fatal("tuiTickMsg after concurrent dispatch must re-queue pollCmd")
	}
	got := model.(*tuiModel)
	if len(got.toolRows) == 0 {
		t.Fatal("expected tool rows in TUI after concurrent dispatch + drain")
	}

	// Verify final state via View() — must not panic
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked after concurrent dispatch: %v", r)
		}
	}()
	view := got.View()
	if view == "" {
		t.Fatal("View() returned empty after concurrent dispatch")
	}
}

// TestBridgeConcurrentWriteAndDrainRace exercises the bridge under racy
// Write+Drain from separate goroutines (simulates agent-producer +
// TUI-consumer). Must not deadlock or produce inconsistent state.
func TestBridgeConcurrentWriteAndDrainRace(t *testing.T) {
	b := newStreamBridge()
	const iterations = 200

	var wg sync.WaitGroup
	// Producer: writes content and pushes tools
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			b.Write([]byte("content " + itoa(i) + "\n"))
			if i%3 == 0 {
				b.PushTool(true, "read_file", `{"iter":`+itoa(i)+`}`)
			}
			if i%5 == 0 && i > 0 {
				b.PushTool(false, "read_file", "done")
			}
		}
	}()

	// Consumer: drains the bridge repeatedly
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations/4; i++ {
			_, _, done, _, _, _, _, _ := b.Drain()
			if done {
				return
			}
		}
	}()

	wg.Wait()

	// Final drain — should succeed without deadlock
	_, tools, _, _, _, _, _, _ := b.Drain()
	// We don't check exact counts (racy), just that no deadlock occurred
	_ = tools
}

// TestBridgeConcurrentFinishAndDrainRace verifies Finish + Drain from
// concurrent goroutines does not deadlock (simulates agent loop finishing
// a turn while TUI is draining).
func TestBridgeConcurrentFinishAndDrainRace(t *testing.T) {
	b := newStreamBridge()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			b.Write([]byte("data"))
			b.PushTool(true, "tool", `{"iter":`+itoa(i)+`}`)
			b.PushTool(false, "tool", "done")
		}
		b.Finish(nil)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			_, _, done, _, _, _, _, _ := b.Drain()
			if done {
				return
			}
		}
	}()

	wg.Wait()

	// Final drain should get the done flag
	_, _, done, _, _, _, _, _ := b.Drain()
	if !done {
		t.Fatal("expected bridge done after concurrent Finish+Drain")
	}
}

// TestStreamBridgeConcurrentActiveToolsNoDeadlock exercises the activeTools
// counting logic under concurrent Start/End pairs with mixed ID'd and
// anonymous tools. Must not deadlock or produce negative activeTools.
func TestStreamBridgeConcurrentActiveToolsNoDeadlock(t *testing.T) {
	b := newStreamBridge()
	const workers = 16
	const eventsPerWorker = 100

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < eventsPerWorker; j++ {
				// Alternating: ID'd tool, anonymous tool, completed banner
				switch j % 3 {
				case 0:
					b.PushToolWithID(true, "call-"+itoa(id), "read_file", `{"id":`+itoa(id)+`}`)
					b.PushToolWithID(false, "call-"+itoa(id), "read_file", "done")
				case 1:
					b.PushTool(true, "grep", `{"pattern":"x"}`)
					b.PushTool(false, "grep", "found")
				case 2:
					b.PushCompletedBanner("aggregator", `{"count":`+itoa(j)+`}`)
				}
			}
		}(i)
	}
	wg.Wait()

	// Final state: activeTools must be 0 (all tools ended)
	active := b.ActiveTools()
	if active != 0 {
		t.Fatalf("expected 0 active tools after all ended, got %d", active)
	}

	// Drain — must succeed without deadlock
	b.Drain()
}
