package chatsync

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// settleWindow gives a wrongly eager implementation time to put the request
// it should not have made on the wire. An assertion made with no wait at all
// would pass for any implementation that merely delays its first request, so
// every "nothing may arrive" claim is checked only after this window.
const settleWindow = 300 * time.Millisecond

// TestOpenSessionMakesNoAPIRequestUntilTheFirstEvent pins the laziness
// contract: opening a sync session arms local state only - the outbox, the
// bus subscription, the workers - and puts NOTHING on the wire until the
// first event arrives. An event only exists once a turn starts, so a user who
// starts the CLI and sends no message must leave no trace on the server: no
// session create, no heartbeat, no long poll. The first event both attaches
// (exactly one create) and is delivered, in order.
func TestOpenSessionMakesNoAPIRequestUntilTheFirstEvent(t *testing.T) {
	fake := newFakeAPI(t)
	bus := events.New()

	syncSess, err := OpenSession(context.Background(), bus, "lazy-open-1", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: fake.URL()},
		OutboxDir:       filepath.Join(t.TempDir(), "outbox-lazy-open-1"),
		CreateTitle:     "Lazy Open",
		HeartbeatPeriod: 10 * time.Millisecond,
		EnablePolling:   true,
		PollWaitSeconds: 1,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = syncSess.Stop(context.Background()) })

	time.Sleep(settleWindow)
	if reqs := fake.Requests(); len(reqs) != 0 {
		t.Fatalf("the API saw %d request(s) before any message was sent, want 0: %+v", len(reqs), reqs)
	}

	publishTestTurn(bus, "lazy-open-1", "turn:1")
	waitForSeq(t, syncSess, 3)
	waitForServerEventCount(t, fake, syncSess.SessionID(), 3)

	if got := fake.CreateAttempts(); got != 1 {
		t.Errorf("CreateAttempts = %d, want exactly 1: the first message attaches once", got)
	}
	if got := len(fake.Events(syncSess.SessionID())); got != 3 {
		t.Errorf("the attached session holds %d events, want 3", got)
	}
}

// waitForServerEventCount polls the fake API's own event store, not the
// session's local (projector-assigned) LastSeq: LastSeq advances as soon as
// an event is queued locally, before the background uploader's HTTP round
// trip lands it on the server, so a caller that only waits on LastSeq and
// then immediately reads fake.Events races the upload - flaky exactly under
// heavy scheduling contention (e.g. `go test ./...`), where the uploader
// goroutine can be delayed well past the local assignment.
func waitForServerEventCount(t *testing.T, fake *fakeAPI, id string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(fake.Events(id)) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server holds %d event(s) for %q, want %d within 3s", len(fake.Events(id)), id, want)
}

// TestStopBeforeTheFirstEventNeverTouchesTheAPI pins the shutdown half: a
// sync session opened and closed with no message in between must be a purely
// local object. This is the exact reported defect - start the CLI, send
// nothing, quit - and it must end with zero requests, including from the
// heartbeat and poller a wrongly eager start would have armed.
func TestStopBeforeTheFirstEventNeverTouchesTheAPI(t *testing.T) {
	fake := newFakeAPI(t)
	bus := events.New()

	syncSess, err := OpenSession(context.Background(), bus, "lazy-stop-1", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: fake.URL()},
		OutboxDir:       filepath.Join(t.TempDir(), "outbox-lazy-stop-1"),
		CreateTitle:     "Lazy Stop",
		HeartbeatPeriod: 10 * time.Millisecond,
		EnablePolling:   true,
		PollWaitSeconds: 1,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	time.Sleep(settleWindow)
	if reqs := fake.Requests(); len(reqs) != 0 {
		t.Errorf("the API saw %d request(s) for a session that never got a message, want 0: %+v", len(reqs), reqs)
	}
	if syncSess.Stopped() {
		t.Errorf("an orderly close before the first message latched a terminal stop: %s", syncSess.StopReason())
	}
}

// TestAttachFailureOnTheFirstEventStopsSyncAndSaysSo pins the failure path of
// the deferred attach. Eager attach surfaced a failure as an OpenSession
// error; a deferred one can only say so through the same channel every other
// terminal stop uses - OnStop with a reason, latched on Stopped() - and the
// session must then ignore later events instead of retrying the create for
// the rest of the process.
func TestAttachFailureOnTheFirstEventStopsSyncAndSaysSo(t *testing.T) {
	fake := newFakeAPI(t)
	fake.RejectCreatesWith(http.StatusForbidden, "Forbidden", "attach refused")

	stopped := make(chan string, 1)
	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "lazy-attach-fail", SessionOptions{
		TokenProvider: testTokenProvider,
		ClientOptions: ClientOptions{BaseURL: fake.URL()},
		OutboxDir:     filepath.Join(t.TempDir(), "outbox-lazy-attach-fail"),
		CreateTitle:   "Lazy Attach Failure",
		OnStop:        func(reason string) { stopped <- reason },
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = syncSess.Stop(context.Background()) })

	publishTestTurn(bus, "lazy-attach-fail", "turn:1")
	select {
	case reason := <-stopped:
		if !strings.Contains(reason, "attach") {
			t.Errorf("StopReason = %q, want it to name the failed attach", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnStop never fired after the deferred attach failed")
	}
	if !syncSess.Stopped() {
		t.Error("Stopped() = false after a failed attach; the terminal latch must be set")
	}

	publishTestTurn(bus, "lazy-attach-fail", "turn:2")
	time.Sleep(settleWindow)
	if got := fake.CreateAttempts(); got != 1 {
		t.Errorf("CreateAttempts = %d, want 1; a stopped session must not retry the attach", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(ctx); err != nil {
		t.Errorf("Stop after a failed attach: %v", err)
	}
}

// TestHeartbeatAndPollerStartOnlyAfterTheFirstEvent pins the runner half of
// the contract: the heartbeat and the input long-poll are API traffic too,
// and a session with no message must not produce any. Both runners start as
// part of the deferred attach, so the first message brings the create, the
// first heartbeat and the first poll.
func TestHeartbeatAndPollerStartOnlyAfterTheFirstEvent(t *testing.T) {
	fake := newFakeAPI(t)
	bus := events.New()

	syncSess, err := OpenSession(context.Background(), bus, "lazy-runners-1", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: fake.URL()},
		OutboxDir:       filepath.Join(t.TempDir(), "outbox-lazy-runners-1"),
		CreateTitle:     "Lazy Runners",
		HeartbeatPeriod: 10 * time.Millisecond,
		EnablePolling:   true,
		PollWaitSeconds: 1,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = syncSess.Stop(context.Background()) })

	time.Sleep(settleWindow)
	for _, r := range fake.Requests() {
		if strings.Contains(r.Target, "/heartbeat") || strings.Contains(r.Target, "/inputs/next") {
			t.Fatalf("runner request %s %s fired before any message was sent", r.Method, r.Target)
		}
	}

	publishTestTurn(bus, "lazy-runners-1", "turn:1")
	waitUntil(t, "the first heartbeat after attach", func() bool {
		for _, r := range fake.Requests() {
			if strings.Contains(r.Target, "/heartbeat") {
				return true
			}
		}
		return false
	})
	waitUntil(t, "the first input poll after attach", func() bool {
		for _, r := range fake.Requests() {
			if strings.Contains(r.Target, "/inputs/next") {
				return true
			}
		}
		return false
	})
}

// TestStopWhileAttachingDeliversTheRacedTail pins the shutdown race: Stop may
// arrive while the first event's attach is still in flight. The attach must
// finish, the raced events must be delivered in the final flush, and the
// session must close cleanly rather than losing the tail.
func TestStopWhileAttachingDeliversTheRacedTail(t *testing.T) {
	fake := newFakeAPI(t)
	fake.SetAppendDelay(200 * time.Millisecond)
	bus := events.New()

	syncSess, err := OpenSession(context.Background(), bus, "lazy-race-1", SessionOptions{
		TokenProvider: testTokenProvider,
		ClientOptions: ClientOptions{BaseURL: fake.URL()},
		OutboxDir:     filepath.Join(t.TempDir(), "outbox-lazy-race-1"),
		CreateTitle:   "Lazy Race",
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTestTurn(bus, "lazy-race-1", "turn:1")
	// Mirror the hosts: the bus is flushed BEFORE Stop, so the events are in
	// the session's own queue when the drain runs. The attach's in-flight
	// round trips still race the Stop below - that race is the subject.
	bus.Flush()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := syncSess.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	ids := fake.SessionIDs()
	if len(ids) != 1 {
		t.Fatalf("SessionIDs = %v, want exactly one created session", ids)
	}
	if got := len(fake.Events(ids[0])); got != 3 {
		t.Errorf("the raced tail delivered %d events, want 3", got)
	}
}
