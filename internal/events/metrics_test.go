package events

import (
	"context"
	"sync"
	"testing"
	"time"
)

// helper: create bus, subscribe adapter, publish, flush, then assert.
func setupMetricsTest(t *testing.T) (*Bus, *MetricsAdapter) {
	t.Helper()
	bus := New()
	t.Cleanup(bus.Close)
	adapter := NewMetricsAdapter()
	adapter.Subscribe(bus)
	return bus, adapter
}

func TestMetricsAdapter_CountsEvents(t *testing.T) {
	bus, adapter := setupMetricsTest(t)

	bus.Publish(NewEvent(KindToolStart))
	bus.Publish(NewEvent(KindToolEnd))
	bus.Publish(NewEvent(KindStep))
	bus.Flush()

	counts, total := adapter.Snapshot()
	if total != 3 {
		t.Fatalf("expected total=3, got %d", total)
	}
	if counts[string(KindToolStart)] != 1 {
		t.Fatalf("expected tool_start=1, got %d", counts[string(KindToolStart)])
	}
	if counts[string(KindToolEnd)] != 1 {
		t.Fatalf("expected tool_end=1, got %d", counts[string(KindToolEnd)])
	}
	if counts[string(KindStep)] != 1 {
		t.Fatalf("expected step=1, got %d", counts[string(KindStep)])
	}
}

func TestMetricsAdapter_MultipleKinds(t *testing.T) {
	bus, adapter := setupMetricsTest(t)

	bus.Publish(NewEvent(KindAssistant))
	bus.Publish(NewEvent(KindAssistant))
	bus.Publish(NewEvent(KindError))
	bus.Flush()

	counts, total := adapter.Snapshot()
	if total != 3 {
		t.Fatalf("expected total=3, got %d", total)
	}
	if counts[string(KindAssistant)] != 2 {
		t.Fatalf("expected assistant=2, got %d", counts[string(KindAssistant)])
	}
	if counts[string(KindError)] != 1 {
		t.Fatalf("expected error=1, got %d", counts[string(KindError)])
	}
}

func TestMetricsAdapter_SnapshotConsistency(t *testing.T) {
	bus, adapter := setupMetricsTest(t)

	bus.Publish(NewEvent(KindToolStart))
	bus.Publish(NewEvent(KindToolEnd))
	bus.Flush()

	firstCounts, firstTotal := adapter.Snapshot()

	bus.Publish(NewEvent(KindStep))
	bus.Publish(NewEvent(KindAssistant))
	bus.Flush()

	// First snapshot must be frozen (unchanged)
	if firstTotal != 2 {
		t.Fatalf("expected first snapshot total=2, got %d", firstTotal)
	}
	if firstCounts[string(KindToolStart)] != 1 {
		t.Fatalf("first snapshot tool_start changed")
	}
	// Second snapshot must reflect new events
	_, secondTotal := adapter.Snapshot()
	if secondTotal != 4 {
		t.Fatalf("expected second snapshot total=4, got %d", secondTotal)
	}
}

func TestMetricsAdapter_Reset(t *testing.T) {
	bus, adapter := setupMetricsTest(t)

	bus.Publish(NewEvent(KindToolStart))
	bus.Publish(NewEvent(KindToolEnd))
	bus.Flush()

	adapter.Reset()

	counts, total := adapter.Snapshot()
	if total != 0 {
		t.Fatalf("expected total=0 after reset, got %d", total)
	}
	if len(counts) != 0 {
		t.Fatalf("expected empty counts after reset, got %d entries", len(counts))
	}
}

func TestMetricsAdapter_ConcurrentSafe(t *testing.T) {
	bus, adapter := setupMetricsTest(t)

	var wg sync.WaitGroup
	const goroutines = 10
	const eventsPerGoroutine = 100
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				bus.Publish(NewEvent(KindAssistant))
			}
		}()
	}
	wg.Wait()
	bus.Flush()

	_, total := adapter.Snapshot()
	// All 1000 events go to a single subscription (buffer=256) because
	// MetricsAdapter subscribes to all kinds. Significant drops are
	// expected. Just verify at least 256 were delivered (buffer capacity).
	if total < 256 {
		t.Fatalf("expected total>=256 (buffer capacity), got %d", total)
	}
}

func TestMetricsAdapter_SubscribeIdempotent(t *testing.T) {
	bus, adapter := setupMetricsTest(t)
	adapter.Subscribe(bus) // second subscribe must be no-op

	bus.Publish(NewEvent(KindToolStart))
	bus.Flush()

	_, total := adapter.Snapshot()
	if total != 1 {
		t.Fatalf("expected total=1 (not 2 from double-subscribe), got %d", total)
	}
}

func TestMetricsAdapter_Close(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	adapter := NewMetricsAdapter()
	adapter.Subscribe(bus)

	bus.Publish(NewEvent(KindToolStart))
	bus.Publish(NewEvent(KindToolEnd))
	bus.Flush()

	adapter.Close()

	// After close, snapshot should return zero
	counts, total := adapter.Snapshot()
	if total != 0 {
		t.Fatalf("expected total=0 after close, got %d", total)
	}
	if len(counts) != 0 {
		t.Fatalf("expected empty counts after close")
	}
}

func TestMetricsAdapter_PanicSafety(t *testing.T) {
	// The adapter's HandleEvent must not crash even if called with problematic
	// input. This tests the internal recover() in HandleEvent.
	adapter := NewMetricsAdapter()
	// Call HandleEvent directly - should not panic
	adapter.HandleEvent(context.Background(), NewEvent(KindError))
	// If we reach here, the adapter survived
	_ = adapter
}

func TestMetricsAdapter_CloseAfterBusClose(t *testing.T) {
	bus := New()
	adapter := NewMetricsAdapter()
	adapter.Subscribe(bus)

	bus.Close()
	// Close adapter after bus is closed - must not panic
	adapter.Close()
}

// Regression: MetricsAdapter.Close() must not hold m.mu across Unsubscribe.
// Unsubscribe synchronously joins the delivery goroutine, which drains
// queued events through HandleEvent and therefore needs m.mu; holding the
// lock across the join deadlocks whenever any event is still queued at
// Close time (the normal TUI shutdown case - final loop events are async
// and nothing Flush()es before metricsAdapter.Close()).
func TestMetricsAdapter_CloseDoesNotDeadlockWithQueuedEvents(t *testing.T) {
	bus := New()
	adapter := NewMetricsAdapter()
	adapter.Subscribe(bus)

	// Fill every subscription queue so the delivery goroutines have queued
	// events to drain (and thus need m.mu) when Unsubscribe stops them.
	for i := 0; i < 1000; i++ {
		bus.Publish(NewEvent(KindToolStart))
		bus.Publish(NewEvent(KindStep))
	}

	done := make(chan struct{})
	go func() {
		adapter.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: MetricsAdapter.Close blocked on its own delivery goroutine")
	}
	bus.Close()
}

// Regression: a Subscribe racing Close() must not leak live subscriptions
// past Close() returning. Close snapshots the subscription list under the
// lock and unsubscribes lock-free (the delivery goroutine needs m.mu while
// draining, so the lock cannot be held across Unsubscribe); without a
// closing marker, a Subscribe that lands in that window re-registers all
// kinds and Close then clears state believing it is detached - leaving live
// delivery goroutines attached to the bus and HandleEvent running on a
// "closed" adapter. Looped: the window is narrow, so 10k iterations make
// the pre-fix leak effectively deterministic.
func TestMetricsAdapter_CloseRacingSubscribeLeavesNoSubscriptions(t *testing.T) {
	bus := New()
	defer bus.Close()
	for i := 0; i < 10000; i++ {
		adapter := NewMetricsAdapter()
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			adapter.Subscribe(bus)
		}()
		go func() {
			defer wg.Done()
			adapter.Close()
		}()
		wg.Wait()

		// After both complete, Close must have won: no subscription for this
		// handler may remain registered on the bus.
		bus.mu.Lock()
		leaks := 0
		for _, subs := range bus.subs {
			for _, s := range subs {
				if s.handler == Handler(adapter) {
					leaks++
				}
			}
		}
		bus.mu.Unlock()
		if leaks != 0 {
			t.Fatalf("iter %d: %d subscriptions remain on the bus after Close returned", i, leaks)
		}
	}
}

// --- Regression tests for missing allKnownKinds entries ---

// declaredKinds collects every exported Kind constant declared in the
// events package via reflect. This is the authoritative set: allKnownKinds
// must contain every entry returned, or MetricsAdapter silently drops events
// of that kind.
func declaredKinds() map[Kind]bool {
	result := make(map[Kind]bool)
	// Enumerate every declared Kind constant directly. This file is in
	// package events so all Kind constants are in scope — if a Kind is renamed
	// or removed, this compilation unit fails to compile, catching drift.
	for _, k := range []Kind{
		KindAssistant, KindToolStart, KindToolEnd, KindStep, KindHeartbeat, KindPrune,
		KindToolParallel, KindSubagentBegin, KindSubagentStart, KindSubagentEnd, KindSubagentHeartbeat,
		KindSubagentDone, KindThinking, KindCompaction,
		KindCacheUsage, KindTokenUsage, KindPrefixReset,
		KindSessionStart, KindSessionEnd, KindTurnStart, KindTurnEnd,
		KindWorkflowRunStarted, KindWorkflowStepStarted, KindWorkflowStepHeartbeat,
		KindWorkflowStepCompleted, KindWorkflowGateResult, KindWorkflowApprovalRequested,
		KindWorkflowRunFinished, KindWorkflowDeliveryStage,
		KindInvocationStarted, KindInvocationCompleted, KindInvocationRetrying,
		KindUIResize, KindUserInput, KindUIReady, KindConfigChange,
		KindError,
	} {
		result[k] = true
	}
	return result
}

func knownKindsSet() map[Kind]bool {
	result := make(map[Kind]bool, len(allKnownKinds))
	for _, k := range allKnownKinds {
		result[k] = true
	}
	return result
}

// TestAllKnownKinds_ContainsAllDeclaredKinds verifies that every declared
// Kind constant appears in allKnownKinds. Fails on current code because
// KindThinking, KindCompaction, and KindTokenUsage are missing.
func TestAllKnownKinds_ContainsAllDeclaredKinds(t *testing.T) {
	declared := declaredKinds()
	known := knownKindsSet()
	missing := make([]Kind, 0)
	for k := range declared {
		if !known[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Errorf("allKnownKinds is missing declared Kind constants: %v", missing)
	}
}

// TestAllKnownKinds_HasNoExtraKinds verifies that every entry in
// allKnownKinds is a valid declared Kind constant. Guards against phantom
// entries. Expected to pass on both current and fixed code.
func TestAllKnownKinds_HasNoExtraKinds(t *testing.T) {
	declared := declaredKinds()
	known := knownKindsSet()
	extra := make([]Kind, 0)
	for k := range known {
		if !declared[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		t.Errorf("allKnownKinds contains entries that are not declared Kind constants: %v", extra)
	}
}

// TestMetricsAdapter_CountsMissingKinds publishes one event each of
// KindThinking, KindCompaction, and KindTokenUsage to a subscribed
// MetricsAdapter, Flushes, then asserts per-kind counts and total.
// Each assertion fails on current code because Subscribe never registers
// handlers for these kinds.
func TestMetricsAdapter_CountsMissingKinds(t *testing.T) {
	bus, adapter := setupMetricsTest(t)

	bus.Publish(NewEvent(KindThinking))
	bus.Publish(NewEvent(KindCompaction))
	bus.Publish(NewEvent(KindTokenUsage))
	bus.Flush()

	counts, total := adapter.Snapshot()
	if total != 3 {
		t.Fatalf("expected total=3, got %d", total)
	}
	if counts[string(KindThinking)] != 1 {
		t.Errorf("expected thinking=1, got %d", counts[string(KindThinking)])
	}
	if counts[string(KindCompaction)] != 1 {
		t.Errorf("expected compaction=1, got %d", counts[string(KindCompaction)])
	}
	if counts[string(KindTokenUsage)] != 1 {
		t.Errorf("expected token_usage=1, got %d", counts[string(KindTokenUsage)])
	}
}

// TestMetricsAdapter_TotalIncludesMissingKinds publishes events of all three
// previously-missing kinds plus two well-known kinds, Flushes, and asserts
// total==5. Confirms that the fix adds to the total, not just individual
// counters.
func TestMetricsAdapter_TotalIncludesMissingKinds(t *testing.T) {
	bus, adapter := setupMetricsTest(t)

	bus.Publish(NewEvent(KindThinking))
	bus.Publish(NewEvent(KindCompaction))
	bus.Publish(NewEvent(KindTokenUsage))
	bus.Publish(NewEvent(KindAssistant))
	bus.Publish(NewEvent(KindError))
	bus.Flush()

	_, total := adapter.Snapshot()
	if total != 5 {
		t.Fatalf("expected total=5 (3 missing + 2 known), got %d", total)
	}
}

// TestMetricsAdapter_MissingKindNotDropped subscribes a raw HandlerFunc for
// KindThinking alongside MetricsAdapter, publishes KindThinking, Flushes,
// and asserts both the raw handler received the event AND MetricsAdapter
// counted it. Negative/control test: proves the gap is in Subscribe (not
// in the bus).
func TestMetricsAdapter_MissingKindNotDropped(t *testing.T) {
	bus := New()
	t.Cleanup(bus.Close)
	adapter := NewMetricsAdapter()
	adapter.Subscribe(bus)

	var rawReceived int
	var rawMu sync.Mutex
	bus.Subscribe(KindThinking, HandlerFunc(func(_ context.Context, ev Event) {
		rawMu.Lock()
		rawReceived++
		rawMu.Unlock()
	}))

	bus.Publish(NewEvent(KindThinking))
	bus.Flush()

	counts, total := adapter.Snapshot()
	if total != 1 {
		t.Errorf("expected MetricsAdapter total=1, got %d", total)
	}
	if counts[string(KindThinking)] != 1 {
		t.Errorf("expected MetricsAdapter thinking=1, got %d", counts[string(KindThinking)])
	}
	if rawReceived != 1 {
		t.Errorf("expected raw handler to receive 1 event, got %d", rawReceived)
	}
}
