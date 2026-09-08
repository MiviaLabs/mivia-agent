package chatsync

import (
	"context"
	"encoding/json"
	"errors"
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
