package ledger

// coverage gaps: the conflicting-payload and taken-sequence branches of
// appendEvent (store.go).

import (
	"context"
	"errors"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"testing"
)

func TestMarshalAndAppendConflictingPayloadIsTaskConflict(t *testing.T) {
	s := NewMemoryStore()
	if err := s.marshalAndAppend("run-x", "evt-1", "kind", map[string]int{"a": 1}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// Same event ID, different payload: the logical key was taken, so the
	// retry must surface ErrTaskConflict rather than silently succeed.
	err := s.marshalAndAppend("run-x", "evt-1", "kind", map[string]int{"a": 2})
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("second append error = %v, want ErrTaskConflict", err)
	}
}

func TestAppendTakenSequenceWithFreshIDIsTaskConflict(t *testing.T) {
	// A different event ID colliding on the (run_id, sequence) unique key
	// with no event carrying that ID: the sequence was lost to another
	// writer, and the append must surface ErrTaskConflict. marshalAndAppend
	// auto-allocates the next free sequence, so the collision is driven
	// through appendEvent directly with an explicitly taken sequence.
	backing := storage.NewMemory()
	s := NewStore(backing)
	if err := s.appendEvent(context.Background(), []storage.Event{
		{ID: "evt-a", RunID: "run-y", Sequence: 1, Kind: "kind", Payload: []byte(`{"a":1}`)},
	}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	err := s.appendEvent(context.Background(), []storage.Event{
		{ID: "evt-b", RunID: "run-y", Sequence: 1, Kind: "kind", Payload: []byte(`{"a":1}`)},
	})
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("fresh-ID append onto taken sequence: %v, want ErrTaskConflict", err)
	}
}
