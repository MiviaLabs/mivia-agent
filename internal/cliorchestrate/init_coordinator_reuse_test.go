package cliorchestrate

import (
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// TestInitCoordinator_ConcurrentFirstCallersReuseOneSubscription pins the
// routedCoordinators.LoadOrStore "loaded" branch: two goroutines racing
// InitCoordinator for the SAME dispatcher's first-ever registration both
// build a coordinator, but only one wins coordinators.LoadOrStore(d, c);
// both then race routedCoordinators.LoadOrStore on the winner's
// coordinator, and the loser must see loaded=true and tear its duplicate
// subscription down. A sequential second call never reaches any of this -
// it hits the cache-hit early return at the top of InitCoordinator instead.
//
// routedCoordinators' final size cannot prove the branch ran:
// sync.Map.LoadOrStore guarantees exactly one entry regardless of whether
// the cleanup body executes, so testOnRoutedCoordinatorsLoaded observes the
// branch firing directly instead of inferring it from map size.
//
// A single large-goroutine-count attempt is not reliable under heavy
// scheduler contention: if the scheduler serializes the racing goroutines
// enough, the race window never opens, regardless of goroutine count - an
// ordering problem, not a parallelism-degree one. A blocking rendezvous
// barrier was tried and rejected: pausing every caller unconditionally
// deadlocks a legitimate cache-hit caller if a scheduler-delayed second
// caller never arrives to release it. Retrying the whole attempt against a
// fresh dispatcher needs no synchronization and cannot deadlock.
func TestInitCoordinator_ConcurrentFirstCallersReuseOneSubscription(t *testing.T) {
	const goroutinesPerAttempt = 256
	const maxAttempts = 2000

	prevLoadedHook := testOnRoutedCoordinatorsLoaded
	t.Cleanup(func() { testOnRoutedCoordinatorsLoaded = prevLoadedHook })

	for attempt := 0; attempt < maxAttempts; attempt++ {
		d := runtime.New(runtime.Policy{MaxDepth: 1})
		repo := ledger.NewMemoryLedgerRepository()

		var loadedFired int32
		testOnRoutedCoordinatorsLoaded = func() { atomic.AddInt32(&loadedFired, 1) }

		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make([]*coordinator.Coordinator, goroutinesPerAttempt)
		for i := range results {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				// A handful of scheduler yields widens the interleaving
				// window on a CPU-starved runner (observed: some CI
				// environments effectively serialize goroutines that
				// finish faster than the scheduler's own preemption tick,
				// so every attempt's second-and-later goroutine sees
				// coordinators.Load(d) already populated and never races
				// at all). Gosched costs nothing when real parallelism is
				// already available.
				for y := 0; y < 8; y++ {
					goruntime.Gosched()
				}
				results[i] = InitCoordinator(d, config.DefaultSubagentConfig, repo)
			}(i)
		}
		close(start)
		wg.Wait()

		first := results[0]
		consistent := true
		for _, c := range results {
			if c != first {
				consistent = false
				break
			}
		}
		if !consistent {
			t.Fatalf("attempt %d: goroutines got different coordinators for the same dispatcher", attempt)
		}

		var count int
		routedCoordinators.Range(func(k, v any) bool {
			if k == first {
				count++
			}
			return true
		})
		if count != 1 {
			t.Fatalf("attempt %d: routedCoordinators holds %d entries for the shared coordinator, want exactly 1", attempt, count)
		}

		if atomic.LoadInt32(&loadedFired) > 0 {
			return // the race landed and the branch fired at least once: pinned.
		}
	}
	t.Fatalf("the routedCoordinators loaded branch never fired in %d attempts of %d goroutines each", maxAttempts, goroutinesPerAttempt)
}
