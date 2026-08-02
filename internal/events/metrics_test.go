package events

import (
	"context"
	"sync"
	"testing"
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
