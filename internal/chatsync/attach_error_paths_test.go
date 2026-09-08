package chatsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAttachSession_ForkWhenForeign_CreateFailureSurfaces mirrors
// TestAttachSession_ServerAhead_ForkWhenForeign but makes the fork's
// CreateSession call itself fail, pinning the wrap distinct from the happy
// path that test already covers.
func TestAttachSession_ForkWhenForeign_CreateFailureSurfaces(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/chat-sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-foreign-1", LastSeq: 5, Status: "running"})
	})
	mux.HandleFunc("GET /v1/chat-sessions/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]StoredEvent{
			{Seq: 3, Payload: json.RawMessage(`{"writer_id":"writer-foreign"}`)},
		})
	})
	mux.HandleFunc("POST /v1/chat-sessions/{id}/end", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Session{ID: "sess-foreign-1", Status: "ended"})
	})
	mux.HandleFunc("POST /v1/chat-sessions", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, ClientOptions{BaseURL: srv.URL})
	outboxDir := filepath.Join(t.TempDir(), "outbox")
	ob, _ := OpenOutbox(outboxDir, 100)
	defer ob.Close()
	_ = ob.AdvanceCursor(2)

	_, err := AttachSession(context.Background(), client, ob, CreateSessionParams{Title: "Forked"}, "sess-foreign-1", "writer-me")
	if err == nil {
		t.Fatal("AttachSession accepted a fork whose CreateSession failed")
	} else if !strings.Contains(err.Error(), "fork session") {
		t.Fatalf("err = %v, want the fork-session wrap", err)
	}
}

// TestFlushOutbox_UnflushedEventsErrorSurfaces pins flushOutbox's own read
// guard, using the same events.jsonl-as-directory technique established for
// the outbox package's other non-ErrNotExist open failures.
func TestFlushOutbox_UnflushedEventsErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := ob.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, eventsFileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, eventsFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	client := newTestClient(t, ClientOptions{BaseURL: "http://unused.invalid"})
	if _, err := flushOutbox(context.Background(), client, ob, "sess-1", "batch-1", "writer-1"); err == nil {
		t.Fatal("flushOutbox accepted an outbox whose events file is a directory")
	} else if !strings.Contains(err.Error(), "read unflushed events") {
		t.Fatalf("err = %v, want the read-unflushed wrap", err)
	}
}

// TestRebaseOntoSessionLocked_ResetForForkErrorSurfaces pins the fork-time
// rebase's own guard: a ResetForFork failure (the outbox's rewrite step
// cannot durably swap the file in) must propagate rather than proceed with
// a stale seq base.
func TestRebaseOntoSessionLocked_ResetForForkErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer ob.Close()
	if err := ob.Append(WireEvent{Seq: 1, Type: "turn_start"}); err != nil {
		t.Fatal(err)
	}
	prev := outboxSyncFile
	outboxSyncFile = func(f *os.File) error {
		if strings.HasSuffix(f.Name(), eventsFileName+".tmp") {
			return errors.New("disk is away")
		}
		return prev(f)
	}
	t.Cleanup(func() { outboxSyncFile = prev })

	s := &SyncSession{outbox: ob, projector: NewProjector("sess-1", 0, ProjectorOptions{})}
	if err := s.rebaseOntoSessionLocked("sess-old"); err == nil {
		t.Fatal("rebaseOntoSessionLocked accepted a ResetForFork that could not durably rewrite the outbox")
	} else if !strings.Contains(err.Error(), "reset outbox for fork") {
		t.Fatalf("err = %v, want the reset-outbox wrap", err)
	}
}

// TestTruncateString_NonPositiveBudgetDropsEverything pins the maxBytes<=0
// guard directly: no budget means nothing survives, not a panic or a
// negative-length slice.
func TestTruncateString_NonPositiveBudgetDropsEverything(t *testing.T) {
	kept, spent, total, cut := truncateString("hello", 0)
	if kept != "" || spent != 0 || total != 5 || !cut {
		t.Fatalf("truncateString(_, 0) = (%q, %d, %d, %v), want (\"\", 0, 5, true)", kept, spent, total, cut)
	}
	if _, _, _, cut := truncateString("hello", -1); !cut {
		t.Fatal("truncateString with a negative budget must report a cut")
	}
}

// TestNextRetryBackoff_SaturatesAtCeiling pins the saturation branch: once
// doubling would meet or cross the ceiling, the ceiling is returned exactly
// rather than an over-doubled value.
func TestNextRetryBackoff_SaturatesAtCeiling(t *testing.T) {
	if got := nextRetryBackoff(flushRetryMaxBackoff / 2); got != flushRetryMaxBackoff {
		t.Errorf("nextRetryBackoff(max/2) = %v, want the ceiling %v", got, flushRetryMaxBackoff)
	}
	if got := nextRetryBackoff(flushRetryMaxBackoff); got != flushRetryMaxBackoff {
		t.Errorf("nextRetryBackoff(max) = %v, want it to stay at the ceiling", got)
	}
}

// TestJitterBackoff_SubTwoNanosecondDelayIsReturnedVerbatim pins the
// half<=0 guard: a delay too small to halve is returned unjittered rather
// than passed to rand with a non-positive bound (which would panic).
func TestJitterBackoff_SubTwoNanosecondDelayIsReturnedVerbatim(t *testing.T) {
	if got := jitterBackoff(1); got != 1 {
		t.Errorf("jitterBackoff(1) = %v, want 1 verbatim", got)
	}
	if got := jitterBackoff(0); got != 0 {
		t.Errorf("jitterBackoff(0) = %v, want 0 verbatim", got)
	}
}

// TestSyncSession_FlushNowAfterRemoteEndIsANoOp pins flushNow's earliest
// guard directly.
func TestSyncSession_FlushNowAfterRemoteEndIsANoOp(t *testing.T) {
	s := &SyncSession{}
	s.remoteEnded.Store(true)
	s.flushNow(context.Background()) // must return immediately, no panic on nil client/outbox
}

// TestSyncSession_HandleRemoteEndIsIdempotent pins handleRemoteEnd's
// CompareAndSwap guard: a second call after the first latch must be a
// silent no-op rather than re-running the shutdown goroutine or
// overwriting stopReason.
func TestSyncSession_HandleRemoteEndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer ob.Close()
	s := &SyncSession{outbox: ob, health: newSyncHealth(nil), doneCh: make(chan struct{}), uploaderDone: make(chan struct{})}
	close(s.doneCh)
	close(s.uploaderDone)
	s.handleRemoteEnd(context.Background(), "first")
	first := s.StopReason()
	s.handleRemoteEnd(context.Background(), "second")
	if got := s.StopReason(); got != first {
		t.Errorf("StopReason after a second handleRemoteEnd = %q, want the first latch %q unchanged", got, first)
	}
}

// TestSyncSession_StopTerminally_AuthStopLatchesRemoteEnd pins the
// auth-stop branch, distinct from the default poison path every other
// stopTerminally-driven test exercises.
func TestSyncSession_StopTerminally_AuthStopLatchesRemoteEnd(t *testing.T) {
	dir := t.TempDir()
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer ob.Close()
	s := &SyncSession{outbox: ob, health: newSyncHealth(nil), doneCh: make(chan struct{}), uploaderDone: make(chan struct{})}
	close(s.doneCh)
	close(s.uploaderDone)
	s.stopTerminally(context.Background(), fmt.Errorf("wrapped: %w", ErrAuthStop))
	if !s.Stopped() {
		t.Fatal("stopTerminally with an auth-stop cause did not latch remoteEnded")
	}
	if !strings.Contains(s.StopReason(), "mivia login") && !strings.Contains(s.StopReason(), "sync stopped") {
		t.Errorf("StopReason = %q, want the auth-stop wording", s.StopReason())
	}
}

// TestSyncSession_HandleCreateFailure_AuthStopLatchesRemoteEnd mirrors
// stopTerminally's auth-stop branch for handleCreateFailure's own copy.
func TestSyncSession_HandleCreateFailure_AuthStopLatchesRemoteEnd(t *testing.T) {
	dir := t.TempDir()
	ob, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer ob.Close()
	s := &SyncSession{outbox: ob, health: newSyncHealth(nil), doneCh: make(chan struct{}), uploaderDone: make(chan struct{})}
	close(s.doneCh)
	close(s.uploaderDone)
	s.handleCreateFailure(context.Background(), fmt.Errorf("wrapped: %w", ErrAuthStop))
	if !s.Stopped() {
		t.Fatal("handleCreateFailure with an auth-stop cause did not latch remoteEnded")
	}
}
