package chatsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestFlushFinalPropagatesUnflushedEventsReadError covers flushFinal's own
// UnflushedEvents error propagation (session_flush.go:222-224): a session
// that is attached (so flushFinal reaches the read at all) whose
// events.jsonl is malformed.
func TestFlushFinalPropagatesUnflushedEventsReadError(t *testing.T) {
	dir := t.TempDir()
	outbox, err := OpenOutbox(dir, 100)
	if err != nil {
		t.Fatalf("OpenOutbox: %v", err)
	}
	defer outbox.Close()
	if err := os.WriteFile(filepath.Join(dir, eventsFileName), []byte("not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &SyncSession{outbox: outbox, sessionID: "sess-flush-gap"}
	s.attached.Store(true)

	if err := s.flushFinal(context.Background()); err == nil {
		t.Fatal("flushFinal did not propagate a malformed events.jsonl read error")
	}
}

// TestWithUnsentRangeCoversBothItsOwnErrorBranches covers withUnsentRange's
// own UnflushedEvents error propagation and its empty-unflushed-list branch,
// distinct from flushFinal's own read (a different call site to the same
// underlying read).
func TestWithUnsentRangeCoversBothItsOwnErrorBranches(t *testing.T) {
	cause := errors.New("upload failed")

	t.Run("read error", func(t *testing.T) {
		dir := t.TempDir()
		outbox, err := OpenOutbox(dir, 100)
		if err != nil {
			t.Fatalf("OpenOutbox: %v", err)
		}
		defer outbox.Close()
		if err := os.WriteFile(filepath.Join(dir, eventsFileName), []byte("not json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s := &SyncSession{outbox: outbox}
		err = s.withUnsentRange(cause)
		if err == nil || !errors.Is(err, cause) {
			t.Fatalf("withUnsentRange with a broken outbox = %v, want it to wrap cause (%v)", err, cause)
		}
	})

	t.Run("empty unflushed list", func(t *testing.T) {
		outbox, err := OpenOutbox(t.TempDir(), 100)
		if err != nil {
			t.Fatalf("OpenOutbox: %v", err)
		}
		defer outbox.Close()
		s := &SyncSession{outbox: outbox}
		err = s.withUnsentRange(cause)
		if err == nil || !errors.Is(err, cause) {
			t.Fatalf("withUnsentRange with nothing unflushed = %v, want it to wrap cause (%v)", err, cause)
		}
	})
}
