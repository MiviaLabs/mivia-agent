package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// TestChangesAndEventsSinceAreIncremental checks the two primitives the ledger
// projection catches up with: a cursor probe that reports only what moved, and
// a bounded tail read. Both backends must behave identically.
func TestChangesAndEventsSinceAreIncremental(t *testing.T) {
	backends := map[string]func(t *testing.T) Store{
		"memory": func(*testing.T) Store { return NewMemory() },
		"sqlite": func(t *testing.T) Store {
			s, err := OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	}

	for name, open := range backends {
		t.Run(name, func(t *testing.T) {
			assertChangesAreIncremental(t, open(t))
		})
	}
}

// assertChangesAreIncremental drives one backend through the incremental
// contract: an empty store reports nothing, a probe returns only what moved
// since the cursor, and EventsSince returns only the tail past a watermark.
func assertChangesAreIncremental(t *testing.T, store Store) {
	t.Helper()
	{
		{
			ctx := context.Background()

			// Empty store: nothing changed, cursor is a valid starting point.
			changed, cursor, err := store.Changes(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(changed) != 0 {
				t.Fatalf("empty store: got %d changed runs, want 0", len(changed))
			}

			for i := 1; i <= 3; i++ {
				if err := store.Append(ctx, Event{
					ID: "a-" + itoa(i), RunID: "run-a", Sequence: i,
					Kind: "k", Payload: []byte(`{}`),
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Append(ctx, Event{
				ID: "b-1", RunID: "run-b", Sequence: 1, Kind: "k", Payload: []byte(`{}`),
			}); err != nil {
				t.Fatal(err)
			}

			changed, cursor, err = store.Changes(ctx, cursor)
			if err != nil {
				t.Fatal(err)
			}
			if changed["run-a"] != 3 || changed["run-b"] != 1 || len(changed) != 2 {
				t.Fatalf("changes = %v, want run-a:3 run-b:1", changed)
			}

			// Nothing new: the probe must report nothing.
			changed, cursor, err = store.Changes(ctx, cursor)
			if err != nil {
				t.Fatal(err)
			}
			if len(changed) != 0 {
				t.Fatalf("steady state: got %v, want no changes", changed)
			}

			// One more append reports exactly one run.
			if err := store.Append(ctx, Event{
				ID: "a-4", RunID: "run-a", Sequence: 4, Kind: "k", Payload: []byte(`{}`),
			}); err != nil {
				t.Fatal(err)
			}
			changed, _, err = store.Changes(ctx, cursor)
			if err != nil {
				t.Fatal(err)
			}
			if len(changed) != 1 || changed["run-a"] != 4 {
				t.Fatalf("changes = %v, want only run-a:4", changed)
			}

			// The tail read returns only what follows the given sequence.
			tail, err := store.EventsSince(ctx, "run-a", 2)
			if err != nil {
				t.Fatal(err)
			}
			if len(tail) != 2 || tail[0].Sequence != 3 || tail[1].Sequence != 4 {
				t.Fatalf("EventsSince(run-a, 2) returned %d events, want sequences 3,4", len(tail))
			}
			if empty, err := store.EventsSince(ctx, "run-a", 4); err != nil || len(empty) != 0 {
				t.Fatalf("EventsSince(run-a, 4) = %d events, %v; want 0, nil", len(empty), err)
			}
		}
	}
}
