//go:build livechat

package chatsync

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// fanOutStreamCount is how many concurrent SSE streams the fan-out probe opens.
//
// Each stream is a separate TCP connection, so a load balancer in front of N
// replicas spreads them. With two replicas the chance that all six land on the
// same one is 2*(1/2)^6, about 3%, so "every stream saw the event" is strong
// evidence that delivery crossed a process boundary rather than luck.
const fanOutStreamCount = 6

// TestLiveChatSessionFanOutReachesEveryStream is the multi-replica regression
// test: it fails on exactly the defect that made a second instance unusable.
//
// Subjects and waiters live in per-process maps. Without cross-instance
// fan-out, an append served by one replica reaches only the streams held by
// that same replica, and every other stream sits silent with no error and no
// close. Counting how many of N streams receive one appended event turns that
// into a number instead of a hang.
func TestLiveChatSessionFanOutReachesEveryStream(t *testing.T) {
	ctx := liveContext(t)
	a := newAPI(t, ctx)
	s := a.createSession(ctx, "fan-out")

	// Seed one event so each stream can subscribe past it and expect silence
	// until the real append below.
	a.appendEvents(ctx, s.ID, sampleEvents(1, 1), http.StatusOK)

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	streams := make([]<-chan sseFrame, 0, fanOutStreamCount)
	for i := 0; i < fanOutStreamCount; i++ {
		// Its own client, keep-alives off, so every stream performs a fresh
		// handshake and the balancer is free to place it on any replica.
		isolated := &api{
			t:       t,
			baseURL: a.baseURL,
			bearer:  a.bearer,
			client:  &http.Client{Transport: &http.Transport{DisableKeepAlives: true}},
		}
		streams = append(streams, openSSE(t, streamCtx, isolated, s.ID, "?afterSeq=1"))
	}

	// Let every subscription settle before the write, so a missed frame means
	// the event never arrived rather than that the stream was not yet open.
	time.Sleep(3 * time.Second)

	a.appendEvents(ctx, s.ID, sampleEvents(2, 2), http.StatusOK)

	received := countStreamsThatSaw(streams, 2, 20*time.Second)
	t.Logf("streams that received the appended event: %d of %d", received, fanOutStreamCount)

	if received != fanOutStreamCount {
		t.Fatalf(
			"only %d of %d streams received the event. Delivery did not cross replicas: an append served by one instance reached only the streams that instance happened to hold, which is the failure that makes a second replica silently drop live updates",
			received, fanOutStreamCount,
		)
	}
}

// countStreamsThatSaw reports how many streams delivered an event with the
// wanted seq inside the deadline.
func countStreamsThatSaw(streams []<-chan sseFrame, wantSeq int64, wait time.Duration) int {
	var (
		mu    sync.Mutex
		count int
		wg    sync.WaitGroup
	)
	// One deadline CHANNEL cannot be shared: time.After delivers a single value,
	// so exactly one waiter would wake and the rest would block until their
	// stream closed. A context broadcasts to every waiter instead.
	deadline, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	for _, frames := range streams {
		wg.Add(1)
		go func(frames <-chan sseFrame) {
			defer wg.Done()
			for {
				select {
				case frame, ok := <-frames:
					if !ok {
						return
					}
					if frame.ID == fmt.Sprint(wantSeq) {
						mu.Lock()
						count++
						mu.Unlock()
						return
					}
				case <-deadline.Done():
					return
				}
			}
		}(frames)
	}
	wg.Wait()
	return count
}
