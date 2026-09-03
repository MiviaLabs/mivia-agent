package chatsync

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// blockingAppender holds the first Append until release is closed, then
// delegates every call to the real outbox. It lets a test pin the worker
// inside processEvent while Stop's deadline expires around it.
type blockingAppender struct {
	inner   outboxAppender
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (b *blockingAppender) Append(events ...WireEvent) error {
	b.once.Do(func() {
		close(b.entered)
		<-b.release
	})
	return b.inner.Append(events...)
}

// TestSyncSession_UnsubscribeRunsAfterTheFinalDropsReadOnTimeout is the
// timeout half of TestSyncSession_UnsubscribeRunsAfterTheFinalDropsRead.
//
// That test covers the path where the worker finishes before Stop's deadline.
// This one covers the path where it does not - and that path is the one a
// caller actually hits, because Stop grows a deadline precisely for the case
// where the network is dead and the final append cannot complete.
//
// Stop must not release the subscription while the worker can still read the
// drop counter. events.Subscription documents that a Drops() read after
// Unsubscribe can over-report: a publish already in flight lands in the
// removed subscription's queue and is dropped there, moving the counter
// without anything being lost. drainAndFlushFinal reads that counter at
// session_flush.go:178 and mints a sync.dropped marker from it, and settled
// decision 6 makes the marker PERMANENT. A marker minted in that window is a
// permanent claim about a hole that never existed.
//
// The worker is pinned inside a blocked Append so it is provably still running
// when the deadline expires; releasing it afterwards drives the final drop
// read. Without the fix that read lands after Unsubscribe.
func TestSyncSession_UnsubscribeRunsAfterTheFinalDropsReadOnTimeout(t *testing.T) {
	bus := events.New()
	syncSess := openStallingUnsubTimeoutSession(t, bus)

	var mu sync.Mutex
	var order []string
	record := func(what string) {
		mu.Lock()
		order = append(order, what)
		mu.Unlock()
	}

	blocker := &blockingAppender{release: make(chan struct{}), entered: make(chan struct{})}
	blocker.inner = syncSess.swapAppender(blocker)

	realUnsubscribe := syncSess.swapUnsubscribe(func() { record("unsubscribe") })
	syncSess.swapDropSource(func() uint64 {
		record("drops")
		return 0
	})
	t.Cleanup(realUnsubscribe)

	// Pin the worker: this event's append blocks until we release it.
	publishTurnStart(bus, "sess-unsub-timeout", "turn:1", "content at shutdown")

	// Do not call Stop until the worker is provably inside the blocked
	// Append; otherwise it can finish first and Stop never times out.
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never reached the appender; the test cannot pin it mid-drain")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	stopErr := syncSess.Stop(stopCtx)
	if stopErr == nil {
		t.Fatal("Stop returned nil; this test needs the timeout path and did not reach it")
	}

	// Let the worker finish. Its final drop read happens from here. Count the
	// reads already recorded first: processEvent reads the counter too, so
	// waiting for "any read" would be satisfied before the worker resumes.
	mu.Lock()
	readsBefore := countDropReads(order)
	mu.Unlock()
	close(blocker.release)
	waitForAnotherDropRead(t, &mu, &order, readsBefore)
	// Unsubscribe runs once the worker is done, and the worker now waits for
	// the uploader's in-flight push before it is - a push the stalling server
	// holds for 1.5s. The property under test is the ORDER of the two
	// records, not how soon the second lands, so wait for it (bounded).
	waitForUnsubscribe(t, &mu, &order)

	assertUnsubscribeAfterFinalDropsRead(t, &mu, &order)
}

// openStallingUnsubTimeoutSession opens the session this test pins mid-drain
// against a server that stalls every push for 1.5s, so Stop's short deadline
// reliably takes the timeout path.
func openStallingUnsubTimeoutSession(t *testing.T, bus *events.Bus) *SyncSession {
	t.Helper()
	srv := newStallingServer(t, 1500*time.Millisecond)
	syncSess, err := OpenSession(context.Background(), bus, "sess-unsub-timeout", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Unsubscribe Order On Timeout",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return syncSess
}

// assertUnsubscribeAfterFinalDropsRead checks the property under test: on
// the timeout path, Unsubscribe must run after the last recorded Drops()
// read, never before it.
func assertUnsubscribeAfterFinalDropsRead(t *testing.T, mu *sync.Mutex, order *[]string) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()

	unsubAt, lastDropsAt := shutdownOrderIndices(*order)
	if unsubAt < 0 {
		t.Fatalf("Unsubscribe never ran; order = %v", *order)
	}
	if lastDropsAt < 0 {
		t.Fatalf("the drop counter was never read; order = %v", *order)
	}
	if unsubAt < lastDropsAt {
		t.Errorf("on the TIMEOUT path Unsubscribe ran at index %d, before the final Drops() read at index %d; order = %v. "+
			"Stop released the subscription while the worker could still read the counter, so shutdown can record a "+
			"permanent sync.dropped for a hole that never existed.", unsubAt, lastDropsAt, *order)
	}
}

// shutdownOrderIndices returns where Unsubscribe ran and where the LAST drop
// read happened, or -1 for either that never occurred.
func shutdownOrderIndices(order []string) (unsubAt, lastDropsAt int) {
	unsubAt, lastDropsAt = -1, -1
	for i, what := range order {
		switch what {
		case "unsubscribe":
			unsubAt = i
		case "drops":
			lastDropsAt = i
		}
	}
	return unsubAt, lastDropsAt
}

func countDropReads(order []string) int {
	n := 0
	for _, what := range order {
		if what == "drops" {
			n++
		}
	}
	return n
}

// waitForAnotherDropRead waits until a drop read BEYOND the ones already
// recorded appears, so the assertion sees the worker's final read rather than
// one processEvent made before shutdown began.
func waitForAnotherDropRead(t *testing.T, mu *sync.Mutex, order *[]string, before int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := countDropReads(*order)
		mu.Unlock()
		if n > before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the worker never recorded a drop read after being released")
}

// waitForUnsubscribe waits until the unsubscribe record appears, bounded so a
// worker that never finishes fails the test instead of hanging it.
func waitForUnsubscribe(t *testing.T, mu *sync.Mutex, order *[]string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		unsubAt, _ := shutdownOrderIndices(*order)
		mu.Unlock()
		if unsubAt >= 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Unsubscribe never ran after the worker was released")
}
