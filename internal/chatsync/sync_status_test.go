package chatsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func readStatusFile(t *testing.T, dir string) SyncStatus {
	t.Helper()
	st, err := tryReadStatusFile(dir)
	if err != nil {
		t.Fatalf("%s: %v", statusFileName, err)
	}
	return st
}

func tryReadStatusFile(dir string) (SyncStatus, error) {
	var st SyncStatus
	data, err := os.ReadFile(filepath.Join(dir, statusFileName))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("parse: %w\n%s", err, data)
	}
	return st, nil
}

// waitForStatusState polls status.json until it reads state. It must poll:
// Stopped() flips on the CompareAndSwap one line before the record is
// written.
func waitForStatusState(t *testing.T, dir, state string) SyncStatus {
	t.Helper()
	var last SyncStatus
	waitUntil(t, "status.json to read "+state, func() bool {
		st, err := tryReadStatusFile(dir)
		if err != nil {
			return false
		}
		last = st
		return st.State == state
	})
	return last
}

// TestDegradedNoticeFiresOnceAndRecoveryClears is the S3.3 discriminator for
// the transition rule. A push that keeps failing today retries in silence
// forever: the failure lands in flushNow's default branch, which arms a
// backoff and says nothing to anyone. The host must be told ONCE that sync is
// degraded, ONCE that it recovered, and the outbox directory must carry a
// status file a later reader can open.
//
// Exactly-once is the load-bearing half: a notice per failed attempt would
// scroll the terminal at the backoff rate, and a host that mutes the second
// notice would mute the recovery too.
func TestDegradedNoticeFiresOnceAndRecoveryClears(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("degraded-once")
	dir := t.TempDir()
	f.RejectAppendsWith(http.StatusInternalServerError, "Internal Server Error", "database is away")

	calls := &healthCalls{}
	bus := events.New()
	s, err := OpenSession(context.Background(), bus, id, SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: f.URL()},
		RemoteSessionID: id,
		OutboxDir:       dir,
		MaxUnflushed:    100,
		HeartbeatPeriod: 10 * time.Minute,
		OnDegraded:      calls.degraded,
		OnRecovered:     calls.recovered,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(stopCtx)
	})

	publishTurnStart(bus, id, "turn:1", "the server is down for this")

	waitUntil(t, "the degraded notice", func() bool { return calls.degradedCount() >= 1 })
	// Keep failing past the threshold so a per-failure implementation has a
	// chance to fire again before the assertion. One extra failure, not
	// more: each one doubles the backoff, and the recovery wait below has to
	// outlast the last scheduled retry.
	waitUntil(t, "one more failed push past the threshold", func() bool {
		return len(f.Batches()) >= degradedFailureThreshold+1
	})
	if got := calls.degradedCount(); got != 1 {
		t.Fatalf("OnDegraded fired %d times, want exactly 1: the notice is per transition, not per failed push", got)
	}
	if reason := calls.firstReason(); !strings.Contains(reason, "500") {
		t.Errorf("degraded reason = %q, want it to carry the failure the host cannot otherwise see", reason)
	}
	assertDegradedRecord(t, readStatusFile(t, dir))

	f.ClearAppendRejection()

	waitUntil(t, "the recovered notice", func() bool { return calls.recoveredCount() >= 1 })
	waitUntil(t, "the backlog to land", func() bool { return f.LastSeq(id) >= 1 })
	if got := calls.recoveredCount(); got != 1 {
		t.Fatalf("OnRecovered fired %d times, want exactly 1", got)
	}
	if got := calls.degradedCount(); got != 1 {
		t.Fatalf("OnDegraded fired %d times after recovery, want 1", got)
	}
	assertRecoveredRecord(t, readStatusFile(t, dir))
}

// healthCalls records host callbacks, which run on detached goroutines.
type healthCalls struct {
	mu         sync.Mutex
	reasons    []string
	recoveries int
}

func (c *healthCalls) degraded(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reasons = append(c.reasons, reason)
}

func (c *healthCalls) recovered() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recoveries++
}

func (c *healthCalls) degradedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reasons)
}

func (c *healthCalls) recoveredCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recoveries
}

func (c *healthCalls) firstReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reasons) == 0 {
		return ""
	}
	return c.reasons[0]
}

func assertDegradedRecord(t *testing.T, st SyncStatus) {
	t.Helper()
	if st.State != SyncStateDegraded {
		t.Fatalf("status.json state = %q, want %q", st.State, SyncStateDegraded)
	}
	if st.ConsecutiveFailures < degradedFailureThreshold {
		t.Errorf("status.json consecutive_failures = %d, want >= %d", st.ConsecutiveFailures, degradedFailureThreshold)
	}
	if st.Unflushed == 0 {
		t.Errorf("status.json unflushed = 0 while a batch is stuck at the outbox head")
	}
	if st.CreateThrottledUntil != nil {
		t.Errorf("status.json create_throttled_until = %v, want null: no create throttle is engaged", st.CreateThrottledUntil)
	}
}

func assertRecoveredRecord(t *testing.T, st SyncStatus) {
	t.Helper()
	if st.State != SyncStateRecovered {
		t.Fatalf("status.json state after recovery = %q, want %q", st.State, SyncStateRecovered)
	}
	if st.ConsecutiveFailures != 0 {
		t.Errorf("status.json consecutive_failures after recovery = %d, want 0", st.ConsecutiveFailures)
	}
	if st.LastSuccessAt == nil {
		t.Errorf("status.json last_success_at is null after a successful push")
	}
}

// TestStatusFileSurvivesProcessExit pins the artefact half of S3.3. Every
// signal sync has today is process-local: a terminal stop reaches OnStop and
// a getter, and both die with the process. The file is what lets the NEXT
// person diagnose the LAST run - the incident that motivated this work was
// reconstructed from a gap between two files, because nothing had written
// why sync stopped.
func TestStatusFileSurvivesProcessExit(t *testing.T) {
	t.Run("terminal stop names the reason", func(t *testing.T) {
		_, srv := newConflictServer(t)
		dir := t.TempDir()
		bus := events.New()
		s, err := OpenSession(context.Background(), bus, "sess-409-status", SessionOptions{
			TokenProvider:   testTokenProvider,
			ClientOptions:   ClientOptions{BaseURL: srv.URL},
			OutboxDir:       dir,
			CreateTitle:     "Status Session",
			HeartbeatPeriod: 10 * time.Minute,
		})
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		publishTurnStart(bus, "sess-409-status", "turn:1", "hello")
		waitUntil(t, "the 409 to latch", s.Stopped)

		// BEFORE Stop: the worker's own record is what survives a process
		// killed between the latch and an orderly Stop, which is the case
		// the file exists for. Stop re-derives the same reason, so reading
		// only after Stop would pass with the worker-side write deleted.
		latched := waitForStatusState(t, dir, SyncStateStopped)
		if !strings.Contains(latched.Reason, "409") {
			t.Fatalf("reason at latch = %q, want the 409 the worker latched on", latched.Reason)
		}

		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(stopCtx)

		st := readStatusFile(t, dir)
		if st.State != SyncStateStopped {
			t.Fatalf("state = %q, want %q", st.State, SyncStateStopped)
		}
		if !strings.Contains(st.Reason, "409") {
			t.Errorf("reason = %q, want the terminal reason the process died with", st.Reason)
		}
		if st.Unflushed == 0 {
			t.Errorf("unflushed = 0, want the backlog the 409 stranded")
		}
	})

	t.Run("orderly stop is recorded too", func(t *testing.T) {
		f := newFakeAPI(t)
		id := f.NewSession("orderly")
		dir := t.TempDir()
		_, s := openAgainstFake(t, f, id, dir)

		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Stop(stopCtx); err != nil {
			t.Fatalf("Stop: %v", err)
		}

		st := readStatusFile(t, dir)
		if st.State != SyncStateStopped {
			t.Fatalf("state = %q, want %q", st.State, SyncStateStopped)
		}
		if st.Reason == "" {
			t.Errorf("reason is empty; an orderly stop must still say it was orderly")
		}
	})

	t.Run("a healthy open writes the file at once", func(t *testing.T) {
		f := newFakeAPI(t)
		id := f.NewSession("fresh")
		dir := t.TempDir()
		openAgainstFake(t, f, id, dir)

		st := readStatusFile(t, dir)
		if st.State != SyncStateHealthy {
			t.Fatalf("state = %q, want %q", st.State, SyncStateHealthy)
		}
		if st.LastSuccessAt == nil {
			t.Errorf("last_success_at is null after a successful attach")
		}
	})
}

// fakeStatusSink counts writes so a test can prove "on transition only"
// directly, without racing a real backoff schedule.
type fakeStatusSink struct {
	writes []SyncStatus
}

func (k *fakeStatusSink) write(st SyncStatus) error {
	k.writes = append(k.writes, st)
	return nil
}

func newTrackerAt(start time.Time) (*syncHealth, *fakeStatusSink, *time.Time) {
	now := start
	sink := &fakeStatusSink{}
	h := newSyncHealth(sink.write)
	h.now = func() time.Time { return now }
	return h, sink, &now
}

// TestSyncHealthWritesOnTransitionOnly holds the write discipline the file
// depends on: a write per failed push would rewrite the file at the backoff
// rate, and the sabotage in the plan - "fire per-failure instead of
// per-transition" - must fail here.
func TestSyncHealthWritesOnTransitionOnly(t *testing.T) {
	h, sink, now := newTrackerAt(time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC))
	h.noteOpen(0)
	if len(sink.writes) != 1 || sink.writes[0].State != SyncStateHealthy {
		t.Fatalf("open wrote %+v, want one healthy record", sink.writes)
	}

	pushErr := errors.New("boom")
	var transitions []string
	for i := 0; i < degradedFailureThreshold+3; i++ {
		*now = now.Add(time.Second)
		if tr := h.noteFailure(pushErr, 7); tr != "" {
			transitions = append(transitions, tr)
		}
	}
	if len(transitions) != 1 || transitions[0] != SyncStateDegraded {
		t.Fatalf("transitions over %d failures = %v, want exactly one degraded", degradedFailureThreshold+3, transitions)
	}
	if len(sink.writes) != 2 {
		t.Fatalf("%d writes after repeated failures, want 2 (open + degraded)", len(sink.writes))
	}
	if got := sink.writes[1]; got.ConsecutiveFailures != degradedFailureThreshold || got.Unflushed != 7 {
		t.Errorf("degraded record = %+v, want consecutive_failures=%d unflushed=7", got, degradedFailureThreshold)
	}

	for i := 0; i < 3; i++ {
		if tr := h.noteSuccess(0); i == 0 && tr != SyncStateRecovered {
			t.Fatalf("first success after degraded returned %q, want %q", tr, SyncStateRecovered)
		} else if i > 0 && tr != "" {
			t.Fatalf("a further success returned %q, want no transition", tr)
		}
	}
	if len(sink.writes) != 3 || sink.writes[2].State != SyncStateRecovered {
		t.Fatalf("writes after recovery = %+v, want a third, recovered record", sink.writes)
	}

	if tr := h.noteStop("done", 0); tr != SyncStateStopped {
		t.Fatalf("noteStop returned %q, want %q", tr, SyncStateStopped)
	}
	if tr := h.noteStop("again", 0); tr != "" {
		t.Fatalf("a second noteStop returned %q, want none: the first reason must survive", tr)
	}
	if tr := h.noteFailure(pushErr, 1); tr != "" {
		t.Fatalf("a failure after stop returned %q, want none", tr)
	}
	last := sink.writes[len(sink.writes)-1]
	if last.State != SyncStateStopped || last.Reason != "done" {
		t.Fatalf("final record = %+v, want stopped with the first reason", last)
	}
}

// TestSyncHealthDegradesAfterSixtySecondsOfSilence pins the second arm of the
// threshold. Backoff saturates at 30s, so a single failure after a long quiet
// stretch would need 90s more to reach three failures; the elapsed-time arm
// says a session that has not pushed for a minute is degraded now.
func TestSyncHealthDegradesAfterSixtySecondsOfSilence(t *testing.T) {
	h, sink, now := newTrackerAt(time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC))
	h.noteOpen(0)

	*now = now.Add(degradedSilenceThreshold - time.Second)
	if tr := h.noteFailure(errors.New("first"), 1); tr != "" {
		t.Fatalf("one failure %v after open transitioned to %q, want nothing yet", degradedSilenceThreshold-time.Second, tr)
	}
	*now = now.Add(2 * time.Second)
	if tr := h.noteFailure(errors.New("second"), 1); tr != SyncStateDegraded {
		t.Fatalf("a failure %v after the last success returned %q, want %q", degradedSilenceThreshold+time.Second, tr, SyncStateDegraded)
	}
	got := sink.writes[len(sink.writes)-1]
	if got.ConsecutiveFailures != 2 {
		t.Errorf("consecutive_failures = %d, want 2: the time arm fired, not the count arm", got.ConsecutiveFailures)
	}
	if !strings.Contains(got.Reason, "second") {
		t.Errorf("reason = %q, want the failure that tripped the threshold", got.Reason)
	}
}

// TestWriteFileAtomicLeavesNoPartialFile is the sabotage guard for the write
// discipline: status.json is read by someone diagnosing a stall, possibly
// while the process is mid-write, so it must never be observable half-written.
func TestWriteFileAtomicLeavesNoPartialFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeFileAtomic(dir, "status.json", []byte(`{"state":"healthy"}`)); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	if err := writeFileAtomic(dir, "status.json", []byte(`{"state":"degraded"}`)); err != nil {
		t.Fatalf("writeFileAtomic (overwrite): %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "status.json" {
		t.Fatalf("dir holds %v, want only status.json: a leftover temp file means the rename discipline broke", entries)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "status.json"))
	if string(data) != `{"state":"degraded"}` {
		t.Fatalf("content = %s, want the second write, whole", data)
	}

	// A sync failure must leave the previous file intact rather than a
	// truncated new one.
	prev := outboxSyncFile
	outboxSyncFile = func(*os.File) error { return errors.New("disk is away") }
	t.Cleanup(func() { outboxSyncFile = prev })
	if err := writeFileAtomic(dir, "status.json", []byte(`{"state":"stopped"}`)); err == nil {
		t.Fatal("writeFileAtomic returned nil on a failed fsync")
	}
	data, _ = os.ReadFile(filepath.Join(dir, "status.json"))
	if string(data) != `{"state":"degraded"}` {
		t.Fatalf("content after a failed write = %s, want the previous record untouched", data)
	}
}

// TestStatusWriteFailureNeverBreaksSync holds R8: a status file that cannot be
// written is a lost diagnostic, not a lost session. The count of pushes must
// be unaffected and the callback must still fire.
func TestStatusWriteFailureNeverBreaksSync(t *testing.T) {
	h, _, now := newTrackerAt(time.Now())
	h.write = func(SyncStatus) error { return errors.New("read-only filesystem") }
	h.noteOpen(0)
	fired := 0
	for i := 0; i < degradedFailureThreshold; i++ {
		*now = now.Add(time.Second)
		if h.noteFailure(errors.New("x"), 1) == SyncStateDegraded {
			fired++
		}
	}
	if fired != 1 {
		t.Fatalf("degraded transition reported %d times with a failing writer, want 1", fired)
	}
}

// TestStatusFileRecordsATimedOutStop covers the Stop path the other two do
// not: a final drain that overruns the caller's deadline. That is the dead
// network case the file exists to diagnose, and it must not be left reading
// healthy because Stop returned early and handed the close to a goroutine.
func TestStatusFileRecordsATimedOutStop(t *testing.T) {
	srv := newStallingServer(t, 1500*time.Millisecond)
	dir := t.TempDir()
	bus := events.New()
	s, err := OpenSession(context.Background(), bus, "sess-status-timeout", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       dir,
		MaxUnflushed:    100,
		CreateTitle:     "Status On Timeout",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	blocker := &blockingAppender{release: make(chan struct{}), entered: make(chan struct{})}
	blocker.inner = s.swapAppender(blocker)

	publishTurnStart(bus, "sess-status-timeout", "turn:1", "content at shutdown")
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker never reached the appender")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.Stop(stopCtx); err == nil {
		t.Fatal("Stop returned nil; this test needs the timeout path")
	}
	if st, err := tryReadStatusFile(dir); err == nil && st.State == SyncStateStopped {
		t.Fatalf("status.json read stopped while the worker was still pinned: %+v", st)
	}
	close(blocker.release)

	st := waitForStatusState(t, dir, SyncStateStopped)
	if !strings.Contains(st.Reason, "timed out") {
		t.Errorf("reason = %q, want it to say the drain timed out", st.Reason)
	}
}
