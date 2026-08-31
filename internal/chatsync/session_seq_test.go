package chatsync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

// errSimulatedDisk is a NON-overflow outbox failure: the shape of a failed
// write() or fsync() on events.jsonl.
var errSimulatedDisk = errors.New("chatsync: sync events file: simulated disk failure")

// scriptedAppender wraps the real outbox so a test can make one append fail
// (or stall) without touching the file the outbox owns.
type scriptedAppender struct {
	real  outboxAppender
	fail  atomic.Bool
	stall atomic.Pointer[chan struct{}]
	// entered signals that an Append is now inside the stall, so a test can
	// act while the writer holds whatever the append path holds.
	entered chan struct{}
}

func (a *scriptedAppender) Append(events ...WireEvent) error {
	if ch := a.stall.Load(); ch != nil {
		select {
		case a.entered <- struct{}{}:
		default:
		}
		<-*ch
	}
	if a.fail.Load() {
		return errSimulatedDisk
	}
	return a.real.Append(events...)
}

// swapAppender installs a substitute outbox writer and returns the previous
// one. Test-only; it takes s.mu because the worker goroutine reads the field.
func (s *SyncSession) swapAppender(a outboxAppender) outboxAppender {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.appender
	s.appender = a
	return prev
}

// swapDropSource installs a substitute pre-projection loss counter. Test-only.
func (s *SyncSession) swapDropSource(fn func() uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropSource = fn
}

// interceptAppends installs a scriptedAppender over the live outbox.
func interceptAppends(s *SyncSession) *scriptedAppender {
	a := &scriptedAppender{entered: make(chan struct{}, 1)}
	a.real = s.swapAppender(a)
	return a
}

type recordingServer struct {
	mu     sync.Mutex
	events []EventItem
}

func (r *recordingServer) seqs() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int64, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.Seq)
	}
	return out
}

func newRecordingServer(t *testing.T, id string) (*recordingServer, *httptest.Server) {
	t.Helper()
	rec := &recordingServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Session{ID: id, Status: "running", LastSeq: 0})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Events []EventItem `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		rec.mu.Lock()
		rec.events = append(rec.events, req.Events...)
		rec.mu.Unlock()
		last := int64(0)
		if len(req.Events) > 0 {
			last = req.Events[len(req.Events)-1].Seq
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AppendResult{InsertedCount: len(req.Events), LastSeq: last})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: r.PathValue("id"), Status: "running"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return rec, srv
}

func publishTurnStart(bus *events.Bus, sessionID, turnID, detail string) {
	bus.Publish(events.Event{
		Kind:      events.KindTurnStart,
		SessionID: sessionID,
		TurnID:    turnID,
		Detail:    detail,
		Timestamp: time.Now(),
	})
}

// TestProcessEvent_NonOverflowOutboxErrorDoesNotConsumeSeq proves the seq
// counter tracks what the outbox STORED, not what the projector assigned.
// A consumed-but-unwritten seq makes the remote stream permanently
// non-contiguous, so every later append 400s for the rest of the process.
func TestProcessEvent_NonOverflowOutboxErrorDoesNotConsumeSeq(t *testing.T) {
	rec, srv := newRecordingServer(t, "sess-seq-1")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-seq-1", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Seq Rollback",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTurnStart(bus, "sess-seq-1", "turn:1", "first")
	waitForSeq(t, syncSess, 1)

	// The outbox now fails with a plain write/fsync error - NOT an overflow.
	a := interceptAppends(syncSess)
	a.fail.Store(true)
	publishTurnStart(bus, "sess-seq-1", "turn:2", "lost to a disk error")
	time.Sleep(150 * time.Millisecond)

	if got := syncSess.LastSeq(); got != 1 {
		t.Errorf("LastSeq() after a non-overflow outbox error = %d, want 1 "+
			"(an unwritten seq must not be consumed)", got)
	}

	// Recovery: the next stored event must continue the stream with no hole.
	a.fail.Store(false)
	publishTurnStart(bus, "sess-seq-1", "turn:3", "after recovery")
	waitForSeq(t, syncSess, 2)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	assertContiguousFrom1(t, rec.seqs())
}

// TestDrainAndFlushFinal_OutboxErrorDoesNotConsumeSeq covers the second
// rollback site: the final drop-marker flush on shutdown.
func TestDrainAndFlushFinal_OutboxErrorDoesNotConsumeSeq(t *testing.T) {
	_, srv := newRecordingServer(t, "sess-seq-2")

	bus := events.New()
	syncSess, err := OpenSession(context.Background(), bus, "sess-seq-2", SessionOptions{
		TokenProvider:   testTokenProvider,
		ClientOptions:   ClientOptions{BaseURL: srv.URL},
		OutboxDir:       t.TempDir(),
		MaxUnflushed:    100,
		CreateTitle:     "Seq Rollback Drain",
		HeartbeatPeriod: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	publishTurnStart(bus, "sess-seq-2", "turn:1", "first")
	waitForSeq(t, syncSess, 1)

	// Report loss so the final Flush emits a sync.dropped marker, and make the
	// outbox reject it.
	syncSess.swapDropSource(func() uint64 { return 7 })
	a := interceptAppends(syncSess)
	a.fail.Store(true)

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := syncSess.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := syncSess.LastSeq(); got != 1 {
		t.Errorf("LastSeq() after a failed final drop-marker append = %d, want 1 "+
			"(an unwritten seq must not be consumed)", got)
	}
}

func waitForSeq(t *testing.T, s *SyncSession, want int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.LastSeq() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("LastSeq() = %d, want %d within 3s", s.LastSeq(), want)
}

func assertContiguousFrom1(t *testing.T, seqs []int64) {
	t.Helper()
	if len(seqs) == 0 {
		t.Fatal("server received no events")
	}
	seen := map[int64]bool{}
	var maxSeq int64
	for _, s := range seqs {
		seen[s] = true
		if s > maxSeq {
			maxSeq = s
		}
	}
	for i := int64(1); i <= maxSeq; i++ {
		if !seen[i] {
			t.Fatalf("stream is not contiguous: seq %d missing from %v", i, seqs)
		}
	}
}
