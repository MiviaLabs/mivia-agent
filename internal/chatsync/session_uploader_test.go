package chatsync

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// openStreaming opens a session against the fake with assistant streaming on,
// which is the configuration the defect was measured under: one wire event
// per model delta.
func openStreaming(t *testing.T, f *fakeAPI, remoteID string, maxUnflushed int) (*events.Bus, *SyncSession) {
	t.Helper()
	bus := events.New()
	s, err := OpenSession(context.Background(), bus, remoteID, SessionOptions{
		TokenProvider:    testTokenProvider,
		ClientOptions:    ClientOptions{BaseURL: f.URL()},
		ProjectorOptions: ProjectorOptions{StreamAssistant: true},
		RemoteSessionID:  remoteID,
		OutboxDir:        t.TempDir(),
		MaxUnflushed:     maxUnflushed,
		CreateTitle:      "Uploader",
		HeartbeatPeriod:  10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), RecommendedStopTimeout)
		defer cancel()
		_ = s.Stop(stopCtx)
	})
	return bus, s
}

// publishDeltas streams n four-byte assistant deltas on one turn.
func publishDeltas(bus *events.Bus, sessionID, turn string, n int) {
	for i := 0; i < n; i++ {
		bus.Publish(events.Event{
			Kind:      events.KindAssistant,
			SessionID: sessionID,
			TurnID:    turn,
			Detail:    "delta",
			Content:   "abcd",
			Timestamp: time.Now(),
		})
	}
}

func waitForSeqAtLeast(t *testing.T, s *SyncSession, want int64, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	deadline := start.Add(within)
	for time.Now().Before(deadline) {
		if s.LastSeq() >= want {
			return time.Since(start)
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("LastSeq() = %d, want >= %d within %s", s.LastSeq(), want, within)
	return 0
}

func serverSeqs(f *fakeAPI, id string) []int64 {
	evs := f.Events(id)
	out := make([]int64, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Seq)
	}
	return out
}

func batchStats(f *fakeAPI) (posts, largest, sent int) {
	for _, b := range f.Batches() {
		posts++
		sent += len(b)
		if len(b) > largest {
			largest = len(b)
		}
	}
	return posts, largest, sent
}

func countType(evs []StoredEvent, typ string) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// TestStreamingIsNotSerializedOnTheUploadRoundTrip is the measured defect. A
// single goroutine projected one event, then pushed it and waited one HTTP
// round trip before projecting the next, so 1000 deltas took 1000 round
// trips to reach even the LOCAL outbox. With the push on its own goroutine
// the worker appends at disk speed and the uploader batches what arrived
// during its last round trip: all 1000 land in the outbox in well under two
// seconds and cross the wire in a handful of POSTs.
//
// On the single-goroutine loop this fails at the first assertion: at 200ms
// a round trip, 1000 appends need ~200s.
func TestStreamingIsNotSerializedOnTheUploadRoundTrip(t *testing.T) {
	const deltas = 1000
	f := newFakeAPI(t)
	id := f.NewSession("streaming-throughput")
	f.SetAppendDelay(200 * time.Millisecond)
	bus, s := openStreaming(t, f, id, 5000)

	start := time.Now()
	publishTurnStart(bus, id, "turn:1", "stream")
	publishDeltas(bus, id, "turn:1", deltas)
	appended := waitForSeqAtLeast(t, s, deltas+1, 5*time.Second)
	if appended > 2*time.Second {
		t.Errorf("all %d deltas reached the outbox after %s, want under 2s: the worker is waiting on the network", deltas, appended)
	}

	waitUntilWithin(t, "every delta to reach the server", 15*time.Second, func() bool {
		return f.LastSeq(id) >= deltas+1
	})
	uploaded := time.Since(start)
	posts, largest, sent := batchStats(f)
	t.Logf("appended %d events locally in %s; uploaded in %s over %d POSTs (largest batch %d, %d events sent)",
		deltas+1, appended, uploaded, posts, largest, sent)

	if largest < 50 {
		t.Errorf("largest batch = %d events, want >= 50: the uploader is not batching the backlog", largest)
	}
	if posts > 60 {
		t.Errorf("%d POSTs for %d events, want a few dozen at most: one round trip per delta", posts, deltas+1)
	}
	evs := f.Events(id)
	if n := countType(evs, TypeSyncDropped); n != 0 {
		t.Errorf("%d sync.dropped markers on the wire, want 0", n)
	}
	if n := countType(evs, TypeAssistantDelta); n != deltas {
		t.Errorf("%d deltas on the wire, want %d", n, deltas)
	}
	assertContiguousFrom1(t, serverSeqs(f, id))
	if sent != len(evs) {
		t.Errorf("%d events sent for %d stored: something was resent", sent, len(evs))
	}
}

// TestCLIFinishesTurnWhileUploadIsSlow exercises the shutdown boundary, not
// only the projection throughput. A completed CLI turn must return control
// while the uploader is still waiting on the API; Stop then performs the
// bounded final drain and leaves no event behind.
func TestCLIFinishesTurnWhileUploadIsSlow(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("slow-final-upload")
	f.SetAppendDelay(350 * time.Millisecond)
	bus, s := openStreaming(t, f, id, 100)

	publishTurnStart(bus, id, "turn:1", "the CLI turn is complete")
	waitForSeqAtLeast(t, s, 1, 2*time.Second)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop after a slow upload: %v", err)
	}
	if got := f.LastSeq(id); got != 1 {
		t.Fatalf("server lastSeq = %d, want 1: the completed turn was not finally drained", got)
	}
}

// TestCLIRestartDrainsUnsentOutboxEvents models a process exit after the
// local append but before the API accepts the upload. The second process uses
// the same durable outbox directory and remote session, then drains the exact
// unsent event after the API is available again.
func TestCLIRestartDrainsUnsentOutboxEvents(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("restart-unsent-outbox")
	outboxDir := t.TempDir()
	f.SetAppendDelay(2 * time.Second)

	bus1 := events.New()
	s1, err := OpenSession(context.Background(), bus1, id, SessionOptions{
		TokenProvider:    testTokenProvider,
		ClientOptions:    ClientOptions{BaseURL: f.URL()},
		ProjectorOptions: ProjectorOptions{StreamAssistant: true},
		RemoteSessionID:  id,
		OutboxDir:        outboxDir,
		MaxUnflushed:     100,
		CreateTitle:      "Restart",
		HeartbeatPeriod:  time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenSession first process: %v", err)
	}
	publishTurnStart(bus1, id, "turn:1", "survive restart")
	waitForSeqAtLeast(t, s1, 1, 2*time.Second)

	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	stopErr := s1.Stop(timeoutCtx)
	timeoutCancel()
	if stopErr == nil {
		t.Fatal("first process Stop returned nil, want bounded timeout while upload is unavailable")
	}
	select {
	case <-s1.shutdownDone:
	case <-time.After(3 * time.Second):
		t.Fatal("first process did not finish closing its outbox")
	}

	f.SetAppendDelay(0)
	bus2 := events.New()
	s2, err := OpenSession(context.Background(), bus2, id, SessionOptions{
		TokenProvider:    testTokenProvider,
		ClientOptions:    ClientOptions{BaseURL: f.URL()},
		ProjectorOptions: ProjectorOptions{StreamAssistant: true},
		RemoteSessionID:  id,
		OutboxDir:        outboxDir,
		MaxUnflushed:     100,
		CreateTitle:      "Restart",
		HeartbeatPeriod:  time.Hour,
	})
	if err != nil {
		t.Fatalf("OpenSession restarted process: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s2.Stop(ctx)
	}()
	// The restart replays the unsent outbox when its first message attaches -
	// the production shape: a user comes back and sends another message. The
	// replayed turn must appear exactly once, and the new turn continues the
	// stream.
	publishTurnStart(bus2, id, "turn:2", "the message that drains the outbox")
	waitUntil(t, "the restarted process to drain its outbox", func() bool {
		return countTurnStartedTurns(f.Events(id), "turn:1") == 1
	})
	if got := countTurnStartedTurns(f.Events(id), "turn:1"); got != 1 {
		t.Fatalf("server stored %d turn.started events for turn:1, want exactly one after restart replay", got)
	}
	if got := countTurnStartedTurns(f.Events(id), "turn:2"); got != 1 {
		t.Errorf("server stored %d turn.started events for turn:2, want 1", got)
	}
}

// countTurnStartedTurns counts turn.started events carrying one turn id.
func countTurnStartedTurns(evs []StoredEvent, turn string) int {
	n := 0
	for _, ev := range evs {
		if ev.Type != TypeTurnStarted {
			continue
		}
		var p struct {
			Turn string `json:"turn"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Turn == turn {
			n++
		}
	}
	return n
}

func waitUntilWithin(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestRejectedBatchIsRepairedWhileTheWorkerKeepsProjecting pins the rollback
// contract under asynchronous upload. The server rejects a batch (409, the
// session ended under it) while the worker is still streaming deltas into
// the outbox, so by the time the uploader repairs - a recovery: id swap,
// outbox rebased onto a fresh session, fork marker appended, projector seq
// reset - the rejected batch is many appends old and the outbox holds more
// than it did when the batch was sent.
//
// The repair takes s.mu, the lock projection takes, so the worker cannot
// assign a seq between the outbox renumbering and ResetSeq. The result must
// be exactly what the synchronous path produced: one contiguous stream on
// the new session from 1, every delta present once with its index intact,
// and the projector's counter equal to the server's mark once quiet.
func TestRejectedBatchIsRepairedWhileTheWorkerKeepsProjecting(t *testing.T) {
	const deltas = 300
	f := newFakeAPI(t)
	a := f.NewSession("rejected-mid-stream")
	f.SetAppendDelay(20 * time.Millisecond)
	bus, s := openStreaming(t, f, a, 5000)

	publishTurnStart(bus, a, "turn:1", "stream")
	publishDeltas(bus, a, "turn:1", deltas/2)
	waitUntil(t, "the first push to land", func() bool { return f.LastSeq(a) >= 1 })
	// End the session under the stream: the next batch is refused with 409
	// while the worker is still projecting the second half, which is paced
	// so that it straddles the rejection and the repair.
	f.EndSession(a)
	go func() {
		for i := 0; i < deltas/2; i++ {
			publishDeltas(bus, a, "turn:1", 1)
			time.Sleep(500 * time.Microsecond)
		}
	}()

	b := waitForSecondSession(t, f)
	// What A acknowledged stays on A; recovery moves the UNFLUSHED backlog,
	// so B must hold exactly the rest - every delta once, across the two.
	onA := countType(f.Events(a), TypeAssistantDelta)
	waitUntilWithin(t, "the rest of the stream to land in the replacement", 15*time.Second, func() bool {
		return countType(f.Events(b), TypeAssistantDelta) >= deltas-onA
	})
	waitUntil(t, "the uploader to go quiet", func() bool {
		return s.outbox.UnflushedCount() == 0
	})

	if s.Stopped() {
		t.Fatalf("sync stopped (%q), want recovery", s.StopReason())
	}
	evs := f.Events(b)
	assertContiguousFrom1(t, serverSeqs(f, b))
	if onB := countType(evs, TypeAssistantDelta); onB <= maxAppendBatch {
		t.Errorf("B holds %d deltas, want more than one batch (%d): the rejected batch was not older than the outbox", onB, maxAppendBatch)
	}
	if m := forkMarkerIn(t, evs); m.NewSessionID != b || m.ForkedFrom != a {
		t.Errorf("fork marker = %+v, want new=%s from=%s", m, b, a)
	}
	if n := countType(evs, TypeSyncDropped); n != 0 {
		t.Errorf("%d sync.dropped markers, want 0: a server rejection is not loss", n)
	}
	if got, want := s.LastSeq(), f.LastSeq(b); got != want {
		t.Errorf("projector LastSeq() = %d, server mark = %d: the repair and the worker disagree on the counter", got, want)
	}
	assertDeltaIndicesInOrder(t, f.Events(a), 0, onA)
	assertDeltaIndicesInOrder(t, evs, onA, deltas)
}

// assertDeltaIndicesInOrder checks the stream counters survived the repair:
// the deltas on the wire carry indices from..n-1 in seq order, with no index
// repeated (a double-counted fragment) or skipped (an over-rolled one).
func assertDeltaIndicesInOrder(t *testing.T, evs []StoredEvent, from, n int) {
	t.Helper()
	next := from
	for _, e := range evs {
		if e.Type != TypeAssistantDelta {
			continue
		}
		var p struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("delta payload: %v", err)
		}
		if p.Index != next {
			t.Fatalf("delta at seq %d carries index %d, want %d", e.Seq, p.Index, next)
		}
		next++
	}
	if next != n {
		t.Errorf("%d deltas on the wire, want %d", next, n)
	}
}

// TestStopDrainsTheBacklogThroughTheUploaderOnce: everything appended before
// Stop reaches the server, in one contiguous run, with nothing sent twice.
// The final flush now runs on the uploader after the worker's drain, and the
// worker waits for it, so Stop's own direct flush finds nothing to resend.
func TestStopDrainsTheBacklogThroughTheUploaderOnce(t *testing.T) {
	const deltas = 300
	f := newFakeAPI(t)
	id := f.NewSession("stop-drains")
	f.SetAppendDelay(50 * time.Millisecond)
	bus, s := openStreaming(t, f, id, 5000)

	publishTurnStart(bus, id, "turn:1", "stream")
	publishDeltas(bus, id, "turn:1", deltas)
	waitForSeqAtLeast(t, s, deltas+1, 5*time.Second)

	stopCtx, cancel := context.WithTimeout(context.Background(), RecommendedStopTimeout)
	defer cancel()
	if err := s.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	evs := f.Events(id)
	if len(evs) != deltas+1 {
		t.Errorf("server holds %d events after Stop, want %d", len(evs), deltas+1)
	}
	assertContiguousFrom1(t, serverSeqs(f, id))
	if _, _, sent := batchStats(f); sent != len(evs) {
		t.Errorf("%d events sent for %d stored: the final flush resent something", sent, len(evs))
	}
	if n := s.outbox.UnflushedCount(); n != 0 {
		t.Errorf("%d events still unflushed after Stop", n)
	}
}

// TestStopCancelsAnInFlightUpload verifies the shutdown ownership contract.
// A normal upload uses the uploader context, so Stop must cancel that context
// before it waits for the worker to release the uploader. Otherwise a slow
// server keeps the uploader alive after Stop's deadline.
func TestStopCancelsAnInFlightUpload(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("stop-cancels-upload")
	f.SetAppendDelay(3 * time.Second)
	dir := t.TempDir()
	bus := events.New()
	s, err := OpenSession(context.Background(), bus, id, SessionOptions{
		TokenProvider:    testTokenProvider,
		ClientOptions:    ClientOptions{BaseURL: f.URL()},
		ProjectorOptions: ProjectorOptions{StreamAssistant: true},
		RemoteSessionID:  id,
		OutboxDir:        dir,
		MaxUnflushed:     100,
		CreateTitle:      "Stop Cancels Upload",
		HeartbeatPeriod:  10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTurnStart(bus, id, "turn:1", "in flight")
	waitForSeqAtLeast(t, s, 1, 5*time.Second)
	waitUntil(t, "the upload request to start", func() bool {
		return countRequests(f, "POST", "/v1/chat-sessions/"+id+"/events") >= 1
	})

	stopCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = s.Stop(stopCtx)

	select {
	case <-s.uploaderDone:
	case <-time.After(1 * time.Second):
		t.Fatal("uploader remained alive after Stop cancelled its in-flight HTTP request")
	}
	select {
	case <-s.shutdownDone:
	case <-time.After(1 * time.Second):
		t.Fatal("shutdown finalizer remained active after the uploader exited")
	}
}

// TestStopReportsTheExactUnsentRange keeps the durable recovery contract
// visible. A failed final upload must not close or advance away the outbox;
// the caller needs both the exact range and the persisted events for retry.
func TestStopReportsTheExactUnsentRange(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("stop-unsent-range")
	f.RejectAppendsWith(http.StatusInternalServerError, "Internal Server Error", "append unavailable")
	dir := t.TempDir()
	bus := events.New()
	s, err := OpenSession(context.Background(), bus, id, SessionOptions{
		TokenProvider:    testTokenProvider,
		ClientOptions:    ClientOptions{BaseURL: f.URL()},
		ProjectorOptions: ProjectorOptions{StreamAssistant: true},
		RemoteSessionID:  id,
		OutboxDir:        dir,
		MaxUnflushed:     100,
		CreateTitle:      "Stop Unsent Range",
		HeartbeatPeriod:  10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTurnStart(bus, id, "turn:1", "unsent")
	publishDeltas(bus, id, "turn:1", 3)
	waitForSeqAtLeast(t, s, 4, 5*time.Second)

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stopErr := s.Stop(stopCtx)
	if stopErr == nil || !strings.Contains(stopErr.Error(), "unsent sequence range 1-4") {
		t.Fatalf("Stop error = %v, want exact unsent range 1-4", stopErr)
	}

	reopened, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("reopen durable outbox: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	unsent, err := reopened.UnflushedEvents()
	if err != nil {
		t.Fatalf("read reopened outbox: %v", err)
	}
	if len(unsent) != 4 || unsent[0].Seq != 1 || unsent[len(unsent)-1].Seq != 4 {
		t.Fatalf("reopened unsent events = %v, want durable range 1-4", unsent)
	}
}

// TestSessionContextCancelReleasesBothGoroutines: a cancelled session
// context unwinds the worker AND the uploader without a hang - the worker
// waits for the uploader's final pass, so an uploader that did not exit
// would wedge Stop - and a later Stop with a live context pushes the backlog
// the cancelled final flush could not.
func TestSessionContextCancelReleasesBothGoroutines(t *testing.T) {
	f := newFakeAPI(t)
	id := f.NewSession("ctx-cancel")
	f.SetAppendDelay(1 * time.Second)
	bus := events.New()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := OpenSession(ctx, bus, id, SessionOptions{
		TokenProvider:    testTokenProvider,
		ClientOptions:    ClientOptions{BaseURL: f.URL()},
		ProjectorOptions: ProjectorOptions{StreamAssistant: true},
		RemoteSessionID:  id,
		OutboxDir:        t.TempDir(),
		MaxUnflushed:     5000,
		HeartbeatPeriod:  10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTurnStart(bus, id, "turn:1", "stream")
	publishDeltas(bus, id, "turn:1", 100)
	waitForSeqAtLeast(t, s, 101, 5*time.Second)
	cancel()

	select {
	case <-s.doneCh:
	case <-time.After(RecommendedStopTimeout):
		t.Fatal("the worker did not unwind after the session context was cancelled: it is stuck waiting for the uploader")
	}
	select {
	case <-s.uploaderDone:
	default:
		t.Fatal("doneCh closed before the uploader returned; the outbox close can race the uploader")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), RecommendedStopTimeout)
	defer stopCancel()
	if err := s.Stop(stopCtx); err == nil || !strings.Contains(err.Error(), "unsent sequence range 1-101") {
		t.Fatalf("Stop error = %v, want the cancelled final upload to report range 1-101", err)
	}
	if got := len(f.Events(id)); got != 0 {
		t.Errorf("server holds %d events, want 0 after the cancelled upload", got)
	}
}

// TestOutboxAppendAndFlushAreConcurrencySafe is the handoff under -race: the
// worker appends while the uploader reads, sends and advances the cursor.
// Every Outbox method both sides touch must serialize on ob.mu, and the
// cursor advance must count what landed during the round trip.
func TestOutboxAppendAndFlushAreConcurrencySafe(t *testing.T) {
	const n = 400
	f := newFakeAPI(t)
	id := f.NewSession("concurrent-outbox")
	client := newTestClient(t, ClientOptions{BaseURL: f.URL()})
	ob, err := OpenOutbox(t.TempDir(), 5000)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer func() { _ = ob.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for seq := int64(1); seq <= n; seq++ {
			if err := ob.Append(WireEvent{
				Seq:     seq,
				Type:    TypeAssistantDelta,
				Payload: &AssistantDeltaPayload{Envelope: Envelope{V: 1, Turn: "turn:1"}, Text: "abcd", Index: int(seq - 1)},
			}); err != nil {
				t.Errorf("Append seq %d: %v", seq, err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := FlushOutbox(context.Background(), client, ob, id); err != nil {
				t.Errorf("FlushOutbox: %v", err)
				return
			}
			_ = ob.UnflushedCount()
			_ = ob.Cursor()
			_ = ob.MaxSeq()
			if f.LastSeq(id) >= n {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Errorf("the server never reached seq %d (at %d)", n, f.LastSeq(id))
	}()
	wg.Wait()

	assertContiguousFrom1(t, serverSeqs(f, id))
	if got := len(f.Events(id)); got != n {
		t.Errorf("server holds %d events, want %d", got, n)
	}
	if got := ob.UnflushedCount(); got != 0 {
		t.Errorf("UnflushedCount() = %d after everything was acked, want 0", got)
	}
}
