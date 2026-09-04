package chatsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// openRecoverable opens a session against the fake with an identity file, so
// a test can assert the identity write-back as well as the stream.
func openRecoverable(t *testing.T, f *fakeAPI, remoteID string, extra func(*SessionOptions)) (*events.Bus, *SyncSession, IdentityRef) {
	t.Helper()
	storeDir := t.TempDir()
	key := IdentityKey("principal-" + remoteID)
	ref := IdentityRef{Dir: IdentityDir(storeDir), Key: key}
	ident, err := LoadOrCreateIdentity(ref.Dir, ref.Key)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}
	bus := events.New()
	opts := SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: f.URL()},
		RemoteSessionID: remoteID,
		OutboxDir:       OutboxDirFor(storeDir, ident.LocalHandle),
		Identity:        ref,
		LocalHandle:     ident.LocalHandle,
		MaxUnflushed:    100,
		CreateTitle:     "Recover",
		HeartbeatPeriod: 10 * time.Minute,
	}
	if extra != nil {
		extra(&opts)
	}
	s, err := OpenSession(context.Background(), bus, remoteID, opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(stopCtx)
	})
	return bus, s, ref
}

func waitForSecondSession(t *testing.T, f *fakeAPI) string {
	t.Helper()
	waitUntil(t, "a replacement session with the backlog in it", func() bool {
		ids := f.SessionIDs()
		return len(ids) >= 2 && f.LastSeq(ids[1]) >= 2
	})
	return f.SessionIDs()[1]
}

func assertRecoveredInto(t *testing.T, f *fakeAPI, s *SyncSession, ref IdentityRef, a, b string) {
	t.Helper()
	if s.Stopped() {
		t.Fatalf("sync stopped (%q), want recovery", s.StopReason())
	}
	evs := f.Events(b)
	if evs[0].Seq != 1 || evs[0].Type != TypeTurnStarted {
		t.Errorf("first event in %s = %s seq %d, want the backlog renumbered from 1", b, evs[0].Type, evs[0].Seq)
	}
	if m := forkMarkerIn(t, evs); m.NewSessionID != b || m.ForkedFrom != a {
		t.Errorf("marker = %+v, want new_session_id=%s forked_from=%s", m, b, a)
	}
	ident, err := LoadOrCreateIdentity(ref.Dir, ref.Key)
	if err != nil {
		t.Fatal(err)
	}
	if ident.RemoteSessionID != b {
		t.Errorf("identity records %q, want %q so the next run re-attaches to the replacement", ident.RemoteSessionID, b)
	}
}

// TestFlushRecoversFromEndedSession: a 409 on every append to A. B is
// created, the backlog lands in B renumbered from 1, B's stream carries a
// sync.forked naming both, and the identity file records B.
func TestFlushRecoversFromEndedSession(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("ended")
	bus, s, ref := openRecoverable(t, f, a, func(o *SessionOptions) {
		o.HeartbeatPeriod = 20 * time.Millisecond
		o.EnablePolling = true
		o.PollWaitSeconds = 1
	})
	f.EndSession(a)
	publishTurnStart(bus, a, "turn:1", "after the web ended it")
	b := waitForSecondSession(t, f)
	assertRecoveredInto(t, f, s, ref, a, b)

	// The heartbeat and the poller follow the backlog to B. A heartbeat left
	// on A would keep a dead session looking alive and let B go stale.
	waitUntil(t, "a heartbeat and a poll against the new session", func() bool {
		return countRequests(f, "POST", "/v1/chat-sessions/"+b+"/heartbeat") >= 1 &&
			countRequests(f, "GET", "/v1/chat-sessions/"+b+"/inputs/next") >= 1
	})
	before := countRequests(f, "POST", "/v1/chat-sessions/"+a+"/heartbeat")
	time.Sleep(100 * time.Millisecond)
	if after := countRequests(f, "POST", "/v1/chat-sessions/"+a+"/heartbeat"); after != before {
		t.Errorf("heartbeats to the abandoned session %s kept arriving after recovery: %d -> %d", a, before, after)
	}
}

// TestRecoveryNamesTheAbandonedSessionOnCreate is the S4.1b discriminator:
// the create request a recovery posts carries the id it is leaving, so the
// server can record the link. The attach-time create does NOT carry it.
func TestRecoveryNamesTheAbandonedSessionOnCreate(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("named")
	bus, _, _ := openRecoverable(t, f, a, nil)
	f.DeleteSession(a)
	publishTurnStart(bus, a, "turn:1", "after the delete")
	waitForSecondSession(t, f)

	var creates []map[string]any
	for _, r := range f.Requests() {
		if r.Method == "POST" && r.Target == "/v1/chat-sessions" {
			var body map[string]any
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatalf("create body: %v", err)
			}
			creates = append(creates, body)
		}
	}
	if len(creates) != 1 {
		t.Fatalf("%d create requests, want 1 (the recovery; the test pre-registered A)", len(creates))
	}
	if got := creates[0]["forkedFromSessionId"]; got != a {
		t.Fatalf("recovery create body forkedFromSessionId = %v, want %q", got, a)
	}
}

func countRequests(f *fakeAPI, method, targetPrefix string) int {
	n := 0
	for _, r := range f.Requests() {
		if r.Method == method && strings.HasPrefix(r.Target, targetPrefix) {
			n++
		}
	}
	return n
}

// armLongRetry drives the retry schedule out past the window a test needs
// to act in, so the worker's ticker cannot flush before Stop does. Three
// transient failures put retryAt 0.5-1s out.
func armLongRetry(t *testing.T, f *fakeAPI, bus *events.Bus, localID, turn string) {
	t.Helper()
	f.RejectAppendsWith(http.StatusInternalServerError, "Internal Server Error", "away")
	publishTurnStart(bus, localID, turn, "held back")
	base := len(f.Batches())
	waitUntil(t, "three transient failures", func() bool { return len(f.Batches()) >= base+3 })
}

// TestFinalFlushOn404LeavesBacklogForRetry: shutdown makes one bounded final
// attempt. A missing remote session is not repaired during shutdown; the
// caller gets the exact unsent range and the durable outbox remains available
// for a later attach/recovery attempt.
func TestFinalFlushOn404LeavesBacklogForRetry(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("final-404")
	bus, s, _ := openRecoverable(t, f, a, nil)
	armLongRetry(t, f, bus, a, "turn:1")
	f.DeleteSession(a)
	f.ClearAppendRejection()

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err == nil || !strings.Contains(err.Error(), "unsent sequence range 1-1") {
		t.Fatalf("Stop error = %v, want exact unsent range 1-1", err)
	}
	ids := f.SessionIDs()
	if len(ids) != 1 {
		t.Fatalf("%d sessions after Stop, want 1: shutdown must not create a replacement session", len(ids))
	}
	if s.Stopped() {
		t.Errorf("stopped: %q", s.StopReason())
	}
}

// TestStopsFinalFlushFailureIsRecorded pins the shutdown failure contract.
// The final upload result must reach status.json with the exact range that
// remains in the durable outbox.
func TestStopsFinalFlushFailureIsRecorded(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("direct-flush")
	bus, s, _ := openRecoverable(t, f, a, nil)
	f.DeleteSession(a)
	publishTurnStart(bus, a, "turn:1", "first recovery")
	b := waitForSecondSession(t, f)

	armLongRetry(t, f, bus, a, "turn:2")
	f.DeleteSession(b)
	f.ClearAppendRejection()

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Stop(stopCtx)

	if s.Stopped() {
		t.Fatalf("stopped (%q): the arrangement must not latch, or the line under test never runs", s.StopReason())
	}
	if n := len(f.SessionIDs()); n != 2 {
		t.Fatalf("%d sessions, want 2: the second recovery inside the interval must be deferred", n)
	}
	st := readStatusFile(t, s.opts.OutboxDir)
	if st.State != SyncStateStopped || !strings.Contains(st.Reason, "final upload failed") || !strings.Contains(st.Reason, "unsent sequence range") {
		t.Fatalf("status.json = %+v, want stopped with the final upload failure and range", st)
	}
	if st.Unflushed == 0 {
		t.Errorf("unflushed = 0, want the backlog the deferred recovery left behind")
	}
}

// TestFlushRecoversFromDeletedSession: the same with a 404, which today
// falls to the default branch and retries forever against a row that cannot
// return.
func TestFlushRecoversFromDeletedSession(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("deleted")
	bus, s, ref := openRecoverable(t, f, a, nil)
	f.DeleteSession(a)
	publishTurnStart(bus, a, "turn:1", "after the web deleted it")
	b := waitForSecondSession(t, f)
	assertRecoveredInto(t, f, s, ref, a, b)
}

// TestTransientFailureDoesNotFork: 500s then success; exactly one session
// ever exists and the backlog lands in it.
func TestTransientFailureDoesNotFork(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("transient")
	bus, s, _ := openRecoverable(t, f, a, nil)
	f.RejectAppendsWith(http.StatusInternalServerError, "Internal Server Error", "away")
	publishTurnStart(bus, a, "turn:1", "queued")
	waitUntil(t, "three failed pushes", func() bool { return len(f.Batches()) >= 3 })
	f.ClearAppendRejection()
	waitUntil(t, "the backlog to land", func() bool { return f.LastSeq(a) >= 1 })
	if n := len(f.SessionIDs()); n != 1 {
		t.Fatalf("%d sessions exist, want 1: a 5xx must never fork", n)
	}
	if s.Stopped() {
		t.Fatalf("stopped: %q", s.StopReason())
	}
}

// TestRepeatedDeletesKeepRecovering: three trigger-then-successful-flush
// cycles. A deliberate web delete is a legitimate, repeatable trigger, and
// a count cap would silence the CLI after three tidy-ups.
func TestRepeatedDeletesKeepRecovering(t *testing.T) {
	prev := recoveryIntervalForTests(t, 50*time.Millisecond)
	defer prev()
	f := newFakeAPI(t)
	a := f.NewSession("repeat")
	cur := a
	bus, s, _ := openRecoverable(t, f, cur, nil)
	for i := 1; i <= 3; i++ {
		f.DeleteSession(cur)
		// The bus is filtered on the LOCAL id, which never changes.
		publishTurnStart(bus, a, "turn:"+string(rune('0'+i)), "again")
		waitUntil(t, "recovery", func() bool {
			ids := f.SessionIDs()
			return len(ids) == i+1 && f.LastSeq(ids[i]) >= 1
		})
		cur = f.SessionIDs()[i]
		if s.Stopped() {
			t.Fatalf("cycle %d: stopped (%q)", i, s.StopReason())
		}
		// A successful push after each recovery resets the no-progress count.
		time.Sleep(60 * time.Millisecond)
	}
}

// TestNoProgressRecoveriesAreBounded: every session 409s. At most two
// recoveries, then sync stops with a reason naming the bound.
func TestNoProgressRecoveriesAreBounded(t *testing.T) {
	prev := recoveryIntervalForTests(t, 20*time.Millisecond)
	defer prev()
	f := newFakeAPI(t)
	a := f.NewSession("no-progress")
	bus, s, _ := openRecoverable(t, f, a, nil)
	f.RejectAppendsWith(http.StatusConflict, "Conflict", "session has ended")
	publishTurnStart(bus, a, "turn:1", "never lands")
	waitUntil(t, "the no-progress bound", s.Stopped)
	if n := len(f.SessionIDs()); n != 1+maxNoProgressRecoveries {
		t.Errorf("%d sessions created, want %d: exactly the bound's recoveries", n, 1+maxNoProgressRecoveries)
	}
	if r := s.StopReason(); !strings.Contains(r, "pushed nothing") {
		t.Errorf("reason = %q, want it to name the no-progress bound", r)
	}
}

// TestIntervalRefusalDefersItDoesNotLatch: two triggers inside the interval.
// The second is deferred and then succeeds once the interval elapses, with
// Stopped() false throughout.
func TestIntervalRefusalDefersItDoesNotLatch(t *testing.T) {
	prev := recoveryIntervalForTests(t, 400*time.Millisecond)
	defer prev()
	f := newFakeAPI(t)
	a := f.NewSession("interval")
	bus, s, _ := openRecoverable(t, f, a, nil)
	f.DeleteSession(a)
	publishTurnStart(bus, a, "turn:1", "one")
	b := waitForSecondSession(t, f)
	f.DeleteSession(b)
	publishTurnStart(bus, a, "turn:2", "two")
	time.Sleep(200 * time.Millisecond)
	if n := len(f.SessionIDs()); n != 2 {
		t.Fatalf("%d sessions after a trigger inside the interval, want 2: the second must be deferred", n)
	}
	if s.Stopped() {
		t.Fatalf("stopped inside the interval: %q", s.StopReason())
	}
	waitUntil(t, "the deferred recovery", func() bool {
		ids := f.SessionIDs()
		return len(ids) == 3 && f.LastSeq(ids[2]) >= 1
	})
	if s.Stopped() {
		t.Fatalf("stopped after the deferred recovery: %q", s.StopReason())
	}
}

// TestAuthStopStillTerminal: a fatal auth failure creates no session.
func TestAuthStopStillTerminal(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("auth")
	var loggedOut atomic.Bool
	bus, s, _ := openRecoverable(t, f, a, func(o *SessionOptions) {
		o.TokenProvider = func(ctx context.Context, force bool) (string, error) {
			if loggedOut.Load() {
				return "", ErrAuthStop
			}
			return testTokenProvider(ctx, force)
		}
	})
	loggedOut.Store(true)
	publishTurnStart(bus, a, "turn:1", "unauthenticated")
	waitUntil(t, "the auth stop", s.Stopped)
	if n := len(f.SessionIDs()); n != 1 {
		t.Errorf("%d sessions, want 1: an auth failure must not mint a session it cannot authenticate to", n)
	}
	if r := s.StopReason(); !strings.Contains(r, "mivia login") {
		t.Errorf("reason = %q", r)
	}
}

// TestDeadOutboxLatchesInsteadOfForking: an outbox a failed rebase left
// unwritable cannot move its backlog anywhere. One latch, zero sessions
// created, reason names the outbox.
func TestDeadOutboxLatchesInsteadOfForking(t *testing.T) {
	// The seam is swapped BEFORE the session opens and armed atomically
	// later: swapping a package var under a running worker is itself a race.
	var diskAway atomic.Bool
	prev := outboxSyncFile
	outboxSyncFile = func(fl *os.File) error {
		if diskAway.Load() && strings.HasSuffix(fl.Name(), eventsFileName+".tmp") {
			return errors.New("disk is away")
		}
		return prev(fl)
	}
	t.Cleanup(func() { outboxSyncFile = prev })

	f := newFakeAPI(t)
	a := f.NewSession("dead-outbox")
	bus, s, _ := openRecoverable(t, f, a, nil)
	publishTurnStart(bus, a, "turn:1", "first")
	waitUntil(t, "the first push", func() bool { return f.LastSeq(a) >= 1 })

	// Kill the outbox the way a failed rebase does: the rewrite's fsync
	// fails once the second event is queued.
	diskAway.Store(true)
	publishTurnStart(bus, a, "turn:2", "second")
	waitUntil(t, "the second event to be queued", func() bool { return s.outbox.UnflushedCount() >= 1 || s.LastSeq() >= 2 })
	if _, err := s.outbox.Rebase(0); err == nil {
		t.Fatal("the rebase was meant to fail and kill the outbox")
	}
	if !s.outbox.Dead() {
		t.Fatal("outbox is not dead after a failed rebase")
	}
	f.DeleteSession(a)
	s.triggerFlush()
	waitUntil(t, "the latch", s.Stopped)
	if n := len(f.SessionIDs()); n != 1 {
		t.Errorf("%d sessions, want 1: nothing may be created for a backlog that cannot move", n)
	}
	if r := s.StopReason(); !strings.Contains(r, "outbox") {
		t.Errorf("reason = %q, want it to name the outbox", r)
	}
}

// TestRecoveryIsRaceFreeUnderConcurrentReaders runs recovery while readers
// hammer SessionID() and LastSeq(). It fails under -race if the rebase
// touches state outside s.mu, and hangs (bounded by the deadline) if
// recovery re-enters the lock.
func TestRecoveryIsRaceFreeUnderConcurrentReaders(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("race")
	bus, s, _ := openRecoverable(t, f, a, nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = s.SessionID()
					_ = s.LastSeq()
				}
			}
		}()
	}
	f.DeleteSession(a)
	publishTurnStart(bus, a, "turn:1", "racing")
	done := make(chan struct{})
	go func() { waitForSecondSession(t, f); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not complete under concurrent readers: deadlock")
	}
	close(stop)
	wg.Wait()
}

// TestRecoveryAbandonsTheNewSessionWhenTheSessionIsAlreadyFinished forces a
// terminal stop into the window between CreateSession and the lock. The
// outbox must NOT be rebased and the id must not be swapped: otherwise an
// orphan remote session is left with an outbox rebased onto an id nobody
// will ever flush.
func TestRecoveryAbandonsTheNewSessionWhenTheSessionIsAlreadyFinished(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("abandon")
	bus, s, _ := openRecoverable(t, f, a, nil)
	s.beforeRecoveryLock = func() {
		s.handleRemoteEnd(context.Background(), "test: finished during recovery")
	}
	f.DeleteSession(a)
	publishTurnStart(bus, a, "turn:1", "in flight")
	waitUntil(t, "the stop", s.Stopped)
	waitUntil(t, "the create to have happened", func() bool { return len(f.SessionIDs()) == 2 })
	if got := s.SessionID(); got != a {
		t.Errorf("SessionID() = %q, want %q: the id must not be swapped after the session finished", got, a)
	}
	b := f.SessionIDs()[1]
	if n := f.LastSeq(b); n != 0 {
		t.Errorf("%d events pushed into the abandoned session %s, want 0", n, b)
	}
	if s.outbox.Cursor().FlushedSeq != 0 || s.outbox.MaxSeq() < 1 {
		t.Errorf("outbox was rebased: cursor=%+v maxSeq=%d", s.outbox.Cursor(), s.outbox.MaxSeq())
	}
}

// TestCreateSessionRejectionDoesNotLatch: the API 400s every create while
// 409-ing appends (the version-skew shape). Sync is not stopped, the outbox
// still holds the whole backlog, and a degraded notice fired.
func TestCreateSessionRejectionDoesNotLatch(t *testing.T) {
	f := newFakeAPI(t)
	a := f.NewSession("create-400")
	var degraded sync.WaitGroup
	degraded.Add(1)
	var once sync.Once
	bus, s, _ := openRecoverable(t, f, a, func(o *SessionOptions) {
		o.OnDegraded = func(string) { once.Do(degraded.Done) }
	})
	f.RejectCreatesWith(http.StatusBadRequest, "Bad Request", "property forkedFromSessionId should not exist")
	f.EndSession(a)
	publishTurnStart(bus, a, "turn:1", "stranded")
	waitUntil(t, "three create attempts", func() bool { return f.CreateAttempts() >= 3 })
	if s.Stopped() {
		t.Fatalf("stopped (%q): a create rejection is not a poisoned body and must never latch", s.StopReason())
	}
	if n := s.outbox.UnflushedCount(); n != 1 {
		t.Errorf("unflushed = %d, want 1: the outbox is untouched until a create succeeds", n)
	}
	if got := s.SessionID(); got != a {
		t.Errorf("SessionID() = %q, want %q unchanged", got, a)
	}
	// With the throttle engaged at its real five-minute period, the next
	// retry is turned away without a request. A refusal is not a failure:
	// the counter must read exactly the three failures that engaged it. This
	// is the leak that is otherwise invisible - a successful create resets
	// the counter anyway.
	waitUntil(t, "a throttle refusal", func() bool { _, refusals := s.throttleCountersForTest(); return refusals >= 1 })
	if failures, _ := s.throttleCountersForTest(); failures != createFailuresBeforeThrottle {
		t.Fatalf("create failure counter = %d after a throttle refusal, want %d: a refused attempt counted as a failure", failures, createFailuresBeforeThrottle)
	}
	if n := f.CreateAttempts(); n != createFailuresBeforeThrottle {
		t.Fatalf("%d create attempts after the throttle engaged, want %d: a refusal must make no request", n, createFailuresBeforeThrottle)
	}
	done := make(chan struct{})
	go func() { degraded.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("no degraded notice for repeated create failures")
	}
}

// TestCreateAttemptsAreBounded asserts ORDERING, not a count: at most three
// attempts before the throttle engages, none inside the (shrunk) throttle
// period, exactly one after it, and a reset plus a landed backlog when that
// one succeeds. (iii) and (iv) are reachable only under a rate bound.
func TestCreateAttemptsAreBounded(t *testing.T) {
	prevPeriod := createThrottlePeriod
	createThrottlePeriod = 600 * time.Millisecond
	t.Cleanup(func() { createThrottlePeriod = prevPeriod })

	f := newFakeAPI(t)
	a := f.NewSession("create-bound")
	bus, s, _ := openRecoverable(t, f, a, nil)
	f.RejectCreatesWith(http.StatusInternalServerError, "Internal Server Error", "away")
	f.EndSession(a)
	publishTurnStart(bus, a, "turn:1", "stranded")

	waitUntil(t, "the throttle to engage", func() bool { return f.CreateAttempts() >= createFailuresBeforeThrottle })
	if n := f.CreateAttempts(); n != createFailuresBeforeThrottle {
		t.Fatalf("(i) %d attempts, want exactly %d before the throttle", n, createFailuresBeforeThrottle)
	}
	// The fake counts the request on receipt; the record is written once the
	// response reaches the client, so wait for the transition rather than
	// reading the file at once.
	st := waitForStatusState(t, s.opts.OutboxDir, SyncStateDegraded)
	if st.CreateThrottledUntil == nil || st.CreateFailures != createFailuresBeforeThrottle {
		t.Errorf("status.json = %+v, want create_throttled_until set and create_failures=%d", st, createFailuresBeforeThrottle)
	}
	time.Sleep(createThrottlePeriod / 2)
	if n := f.CreateAttempts(); n != createFailuresBeforeThrottle {
		t.Fatalf("(ii) %d attempts inside the throttle period, want %d", n, createFailuresBeforeThrottle)
	}
	f.ClearCreateRejection()
	waitUntil(t, "(iii) exactly one attempt after the period", func() bool {
		return f.CreateAttempts() == createFailuresBeforeThrottle+1
	})
	// Judge the timing by the client's own record, not by when this test
	// observed the third request: the fake counts a request on receipt and
	// the client arms the throttle after the response, so a wall-clock
	// reference here can trail the client's by a poll interval.
	if at := f.CreateTimes()[createFailuresBeforeThrottle]; at.Before(*st.CreateThrottledUntil) {
		t.Fatalf("the throttled attempt fired at %v, before create_throttled_until %v", at, *st.CreateThrottledUntil)
	}
	b := waitForSecondSession(t, f)
	if got, _ := s.throttleCountersForTest(); got != 0 {
		t.Errorf("(iv) create failure counter = %d after a successful create, want 0", got)
	}
	if s.Stopped() || s.SessionID() != b {
		t.Errorf("stopped=%v id=%q, want a live session on %s", s.Stopped(), s.SessionID(), b)
	}
}

// recoveryIntervalForTests shrinks the interval refusal so a test can drive
// several recoveries without a real minute between them.
func recoveryIntervalForTests(t *testing.T, d time.Duration) func() {
	t.Helper()
	prev := recoveryInterval
	recoveryInterval = d
	return func() { recoveryInterval = prev }
}

// throttleCountersForTest reads the create throttle's counters: consecutive
// failures and throttle refusals.
func (s *SyncSession) throttleCountersForTest() (failures, refusals int) {
	return int(s.consecutiveCreateFailures.Load()), int(s.createRefusals.Load())
}
