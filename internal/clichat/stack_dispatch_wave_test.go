package clichat

// Concurrent wave dispatch (Phase 2 of
// docs/architecture/spec-auto-split-oversized-prs.md): dispatchWave's
// concurrency and aggregation contract, tested independently of driveChunk's
// full workflow-run fixture requirements (see driveWave's doc comment).
//
// Every concurrency assertion here uses channel-based rendezvous, never
// time.Sleep: a fixed delay only widens the window in which an overlap MIGHT
// be observed, so a real regression (e.g. the semaphore admitting one too
// many) could still slip through on a fast machine. A barrier that blocks
// workers until exactly the expected number have arrived proves the bound
// deterministically instead.

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// awaitOrFail blocks on ch and fails the test if it does not fire within a
// generous deadline, so a real regression in dispatchWave's concurrency
// (e.g. the semaphore under-admitting) hangs the test with a clear failure
// instead of the suite stalling forever.
func awaitOrFail(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal(msg)
	}
}

// TestDispatchWave_RunsAllConcurrently pins bounded fan-out: with
// max_concurrent_chunks >= the wave size, every chunk's work function is
// in flight at the same time, not one at a time. Proven by a rendezvous
// barrier (all n workers must arrive before any proceeds), not a delay.
func TestDispatchWave_RunsAllConcurrently(t *testing.T) {
	const n = 5
	var arrived int32
	allArrived := make(chan struct{})
	release := make(chan struct{})
	var closeOnce sync.Once
	wave := make([]string, n)
	for i := range wave {
		wave[i] = fmt.Sprintf("c%d", i)
	}
	work := func(chunkID string) (bool, error) {
		if atomic.AddInt32(&arrived, 1) == n {
			closeOnce.Do(func() { close(allArrived) })
		}
		<-release
		return false, nil
	}
	done := make(chan []driveWaveResult, 1)
	go func() { done <- dispatchWave(wave, n, work) }()

	awaitOrFail(t, allArrived, "timed out waiting for all workers to start concurrently: dispatchWave did not admit n at once")
	close(release)

	select {
	case results := <-done:
		if len(results) != n {
			t.Fatalf("len(results) = %d, want %d", len(results), n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for dispatchWave to return after release")
	}
}

// TestDispatchWave_BoundedByMaxConcurrent pins the limit: dispatchWave never
// runs more than maxConcurrent work calls at once, even with a larger wave.
// inFlight counts workers currently INSIDE work (incremented on entry,
// decremented via defer before work returns - strictly before dispatchWave's
// own semaphore release, so the count can never exceed the number of
// semaphore tokens actually checked out). A barrier releases the first
// `limit` workers together once all `limit` have arrived, proving the
// semaphore admitted exactly that many concurrently; maxObserved's
// high-water mark is checked after every worker (including the later
// batches the freed slots admit) has run.
func TestDispatchWave_BoundedByMaxConcurrent(t *testing.T) {
	const n = 8
	const limit = 3
	var inFlight, maxObserved int32
	atLimit := make(chan struct{})
	release := make(chan struct{})
	var closeOnce sync.Once
	wave := make([]string, n)
	for i := range wave {
		wave[i] = fmt.Sprintf("c%d", i)
	}
	work := func(chunkID string) (bool, error) {
		cur := atomic.AddInt32(&inFlight, 1)
		defer atomic.AddInt32(&inFlight, -1)
		for {
			max := atomic.LoadInt32(&maxObserved)
			if cur <= max || atomic.CompareAndSwapInt32(&maxObserved, max, cur) {
				break
			}
		}
		if cur == limit {
			closeOnce.Do(func() { close(atLimit) })
		}
		<-release
		return false, nil
	}
	done := make(chan []driveWaveResult, 1)
	go func() { done <- dispatchWave(wave, limit, work) }()

	awaitOrFail(t, atLimit, "timed out waiting for `limit` workers to be admitted concurrently")
	close(release)

	select {
	case results := <-done:
		if len(results) != n {
			t.Fatalf("len(results) = %d, want %d", len(results), n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for dispatchWave to return after release")
	}
	if got := atomic.LoadInt32(&maxObserved); got != limit {
		t.Fatalf("max concurrent in-flight = %d, want exactly %d", got, limit)
	}
}

// TestDispatchWave_ResultOrderMatchesWaveOrder pins deterministic result
// ordering: results[i] always corresponds to wave[i], regardless of which
// goroutine finishes first. Completion order is forced to be the REVERSE of
// wave order via explicit per-chunk release gates, so a bug that returned
// results in completion order (instead of wave order) would be caught
// deterministically rather than depending on scheduling luck.
func TestDispatchWave_ResultOrderMatchesWaveOrder(t *testing.T) {
	wave := []string{"first", "second", "third"}
	gates := map[string]chan struct{}{
		"first": make(chan struct{}), "second": make(chan struct{}), "third": make(chan struct{}),
	}
	work := func(chunkID string) (bool, error) {
		<-gates[chunkID]
		return false, nil
	}
	done := make(chan []driveWaveResult, 1)
	go func() { done <- dispatchWave(wave, len(wave), work) }()

	// Release in reverse wave order: "third" finishes first, "first" last.
	close(gates["third"])
	close(gates["second"])
	close(gates["first"])

	var results []driveWaveResult
	select {
	case results = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for dispatchWave to return")
	}
	for i, r := range results {
		if r.chunkID != wave[i] {
			t.Fatalf("results[%d].chunkID = %q, want %q (wave order, not completion order)", i, r.chunkID, wave[i])
		}
	}
}

// TestDispatchWave_AggregatesErrorsAndHalts pins that every chunk's outcome
// survives independently: one chunk's error or halt does not suppress or
// short-circuit its siblings' results.
func TestDispatchWave_AggregatesErrorsAndHalts(t *testing.T) {
	wave := []string{"ok", "erroring", "halting"}
	boom := errors.New("boom")
	work := func(chunkID string) (bool, error) {
		switch chunkID {
		case "erroring":
			return false, boom
		case "halting":
			return true, nil
		default:
			return false, nil
		}
	}
	results := dispatchWave(wave, len(wave), work)
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	if results[0].err != nil || results[0].halt {
		t.Fatalf("results[0] (ok) = %+v, want no error and no halt", results[0])
	}
	if !errors.Is(results[1].err, boom) {
		t.Fatalf("results[1] (erroring) err = %v, want boom", results[1].err)
	}
	if !results[2].halt || results[2].err != nil {
		t.Fatalf("results[2] (halting) = %+v, want halt=true, err=nil", results[2])
	}
}

// TestDispatchWave_SingleItemMaxConcurrentOne pins the regression guard: a
// wave of exactly one chunk, dispatched with max_concurrent_chunks=1 (the
// default when the workflow declares no override), behaves exactly like a
// direct synchronous call - same result, same shape - so existing
// single-chunk-wave workflows see byte-identical outcomes.
func TestDispatchWave_SingleItemMaxConcurrentOne(t *testing.T) {
	wave := []string{"only"}
	work := func(chunkID string) (bool, error) {
		if chunkID != "only" {
			t.Fatalf("unexpected chunkID %q", chunkID)
		}
		return true, nil
	}
	results := dispatchWave(wave, 1, work)
	if len(results) != 1 || results[0].chunkID != "only" || !results[0].halt || results[0].err != nil {
		t.Fatalf("results = %+v, want single halted no-error result", results)
	}
}

// TestDispatchWave_EmptyWave pins the trivial no-op case.
func TestDispatchWave_EmptyWave(t *testing.T) {
	results := dispatchWave(nil, 4, func(string) (bool, error) {
		t.Fatal("work must not be called for an empty wave")
		return false, nil
	})
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0", len(results))
	}
}

// TestDispatchWave_ZeroOrNegativeMaxConcurrentDefaultsToOne pins the same
// clamp-to-default pattern used elsewhere in this package (clampMax):
// max_concurrent_chunks <= 0 must not mean "unbounded," it must mean
// "sequential," matching the pre-Phase-2 behavior. Uses the same
// inFlight/maxObserved pattern as TestDispatchWave_BoundedByMaxConcurrent
// with limit=1.
func TestDispatchWave_ZeroOrNegativeMaxConcurrentDefaultsToOne(t *testing.T) {
	for _, limit := range []int{0, -1, -100} {
		var inFlight, maxObserved int32
		firstArrived := make(chan struct{})
		release := make(chan struct{})
		var closeOnce sync.Once
		wave := []string{"a", "b", "c"}
		work := func(chunkID string) (bool, error) {
			cur := atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			for {
				max := atomic.LoadInt32(&maxObserved)
				if cur <= max || atomic.CompareAndSwapInt32(&maxObserved, max, cur) {
					break
				}
			}
			closeOnce.Do(func() { close(firstArrived) })
			<-release
			return false, nil
		}
		done := make(chan []driveWaveResult, 1)
		go func() { done <- dispatchWave(wave, limit, work) }()

		awaitOrFail(t, firstArrived, fmt.Sprintf("limit=%d: timed out waiting for the first worker", limit))
		close(release)

		select {
		case results := <-done:
			if len(results) != 3 {
				t.Fatalf("limit=%d: len(results) = %d, want 3", limit, len(results))
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("limit=%d: timed out waiting for dispatchWave to return", limit)
		}
		if got := atomic.LoadInt32(&maxObserved); got != 1 {
			t.Fatalf("limit=%d: max concurrent in-flight = %d, want exactly 1 (sequential default)", limit, got)
		}
	}
}
