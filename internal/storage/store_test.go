package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreContract_AppendIsIdempotentAndOrdered(t *testing.T) {
	ctx := context.Background()
	for name, open := range map[string]func(*testing.T) Store{
		"memory": func(t *testing.T) Store { return NewMemory() },
		"sqlite": func(t *testing.T) Store {
			s, err := OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			first := Event{ID: "e1", RunID: "r1", Sequence: 1, Kind: "user", Payload: []byte(`{"safe":true}`)}
			if err := s.Append(ctx, first); err != nil {
				t.Fatal(err)
			}
			if err := s.Append(ctx, first); !errors.Is(err, ErrDuplicate) {
				t.Fatalf("duplicate err=%v", err)
			}
			if err := s.Append(ctx, Event{ID: "e2", RunID: "r1", Sequence: 2, Kind: "assistant", Payload: []byte(`{"safe":true}`)}); err != nil {
				t.Fatal(err)
			}
			events, err := s.Events(ctx, "r1")
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 || events[0].ID != "e1" || events[1].ID != "e2" {
				t.Fatalf("unexpected events: %+v", events)
			}
		})
	}
}

func TestSQLite_ConcurrentLogicalAgents(t *testing.T) {
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const agents = 200
	var wg sync.WaitGroup
	errs := make(chan error, agents)
	for i := 0; i < agents; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.Append(context.Background(), Event{ID: eventID(i), RunID: runID(i), Sequence: 1, Kind: "agent", Payload: []byte(`{"bounded":true}`)})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.Count(context.Background()); err != nil || got != agents {
		t.Fatalf("count=%d err=%v", got, err)
	}
}

func eventID(i int) string { return "event-" + itoa(i) }
func runID(i int) string   { return "run-" + itoa(i) }
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// TestSQLiteGetClaimErrorAfterClose pins the GetClaim error branch: a closed
// store surfaces the read failure instead of fabricating a claim or a silent
// empty result.
func TestSQLiteGetClaimErrorAfterClose(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "claims.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimRun(ctx, "wfr-x", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetClaim(ctx, "wfr-x"); err == nil {
		t.Fatal("GetClaim on a closed store must error")
	}
}
