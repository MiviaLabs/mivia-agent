package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// collectTurnEvents subscribes to the turn lifecycle kinds and returns a drain
// function.
//
// The returned slice is NOT in publish order across kinds. events.Bus's
// SubscribeMany registers one subscription per kind (bus.go:74), each with its
// own bounded queue, so ordering holds within a kind and not between them - a
// drain of one start and one end routinely yields the end first. Assertions
// here therefore select events by kind rather than by index, and nothing in
// this file may infer a sequence from arrival order.
func collectTurnEvents(t *testing.T, bus *events.Bus, want int) func() []events.Event {
	t.Helper()
	got := make(chan events.Event, 16)
	bus.SubscribeMany(
		[]events.Kind{events.KindTurnStart, events.KindTurnEnd, events.KindError},
		events.HandlerFunc(func(_ context.Context, ev events.Event) { got <- ev }),
	)
	return func() []events.Event {
		var out []events.Event
		deadline := time.After(2 * time.Second)
		for len(out) < want {
			select {
			case ev := <-got:
				out = append(out, ev)
			case <-deadline:
				return out
			}
		}
		// Keep draining briefly after the expected count. Returning the moment
		// `want` is reached would make an EXTRA event invisible, so any
		// "exactly one terminal" assertion could never fail - which is the
		// whole point of those assertions.
		settle := time.After(150 * time.Millisecond)
		for {
			select {
			case ev := <-got:
				out = append(out, ev)
			case <-settle:
				return out
			}
		}
	}
}

func turnEventSession(t *testing.T, completer provider.Completer) (*Session, *events.Bus) {
	t.Helper()
	sess := NewSession(&config.Resolved{Model: "m"}, completer)
	bus := events.New()
	t.Cleanup(bus.Close)
	sess.EventBus = bus
	return sess, bus
}

// TestPlainTurnPublishesStartAndEnd is the regression this whole change exists
// for. Turn boundaries were published by the surface, so only the classic REPL
// and line mode announced them and the TUI announced nothing. Publishing from
// the session means every surface is covered, because every surface reaches
// SendUser.
func TestPlainTurnPublishesStartAndEnd(t *testing.T) {
	sess, bus := turnEventSession(t, &fakeCompleter{out: "reply"})
	drain := collectTurnEvents(t, bus, 2)

	if _, err := sess.SendUser(context.Background(), "hello", nil); err != nil {
		t.Fatalf("SendUser: %v", err)
	}

	starts, ends, errs := partitionByKind(drain())
	if len(starts) != 1 {
		t.Fatalf("got %d turn_start events, want 1", len(starts))
	}
	if len(ends) != 1 {
		t.Fatalf("got %d turn_end events, want 1", len(ends))
	}
	if len(errs) != 0 {
		t.Errorf("a successful turn published %d error events, want 0", len(errs))
	}
	if starts[0].Detail != "hello" {
		t.Errorf("turn_start Detail = %q, want the user's own text", starts[0].Detail)
	}
	if ends[0].Detail != TurnEndCompleted {
		t.Errorf("turn_end Detail = %q, want %q", ends[0].Detail, TurnEndCompleted)
	}
}

// partitionByKind splits a drain by kind, because arrival order across kinds
// is not meaningful. See collectTurnEvents.
func partitionByKind(evs []events.Event) (starts, ends, errs []events.Event) {
	for _, ev := range evs {
		switch ev.Kind {
		case events.KindTurnStart:
			starts = append(starts, ev)
		case events.KindTurnEnd:
			ends = append(ends, ev)
		case events.KindError:
			errs = append(errs, ev)
		}
	}
	return starts, ends, errs
}

// TestTurnStartCarriesTheRealTurnID pins the property that made moving the
// publish worthwhile. internal/hub used to document that KindTurnStart's TurnID
// was "a throwaway, surface-local label (never the same id space as the TurnID
// on every event that follows)", so a consumer could not pair a user's message
// with the reply by id. Both terminals must now share one id.
func TestTurnStartCarriesTheRealTurnID(t *testing.T) {
	sess, bus := turnEventSession(t, &fakeCompleter{out: "reply"})
	drain := collectTurnEvents(t, bus, 2)

	if _, err := sess.SendUser(context.Background(), "hello", nil); err != nil {
		t.Fatalf("SendUser: %v", err)
	}

	starts, ends, _ := partitionByKind(drain())
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("got %d starts and %d ends, want 1 each", len(starts), len(ends))
	}
	if starts[0].TurnID == "" {
		t.Fatal("turn_start carried no TurnID")
	}
	if starts[0].TurnID != ends[0].TurnID {
		t.Errorf("turn_start TurnID %q != terminal TurnID %q; a consumer cannot pair the user's message with its reply", starts[0].TurnID, ends[0].TurnID)
	}
	if _, ok := parseTurnEventID(starts[0].TurnID); !ok {
		t.Errorf("TurnID %q is not the turn:N form later events carry", starts[0].TurnID)
	}
}

// TestConsecutiveTurnsGetDistinctIDs guards the pairing property across turns.
// A shared id would put two exchanges in one bucket.
func TestConsecutiveTurnsGetDistinctIDs(t *testing.T) {
	sess, bus := turnEventSession(t, &fakeCompleter{out: "reply"})
	drain := collectTurnEvents(t, bus, 4)

	for _, text := range []string{"first", "second"} {
		if _, err := sess.SendUser(context.Background(), text, nil); err != nil {
			t.Fatalf("SendUser(%q): %v", text, err)
		}
	}

	starts, ends, _ := partitionByKind(drain())
	if len(starts) != 2 {
		t.Fatalf("got %d turn_start events, want 2", len(starts))
	}
	if starts[0].TurnID == starts[1].TurnID {
		t.Errorf("both turns published TurnID %q; consecutive turns must differ or two exchanges collapse into one", starts[0].TurnID)
	}
	// Each turn's terminal must carry its own id too, or the pairing only
	// works for the first turn.
	endIDs := map[string]bool{}
	for _, ev := range ends {
		endIDs[ev.TurnID] = true
	}
	for _, ev := range starts {
		if !endIDs[ev.TurnID] {
			t.Errorf("turn %q started but no terminal carried that id", ev.TurnID)
		}
	}
}

// TestFailedTurnPublishesErrorNotTurnEnd covers the split terminal. A consumer
// treats turn_end and error as equivalent terminals, so exactly one must fire -
// emitting both would close the turn twice.
func TestFailedTurnPublishesErrorNotTurnEnd(t *testing.T) {
	boom := errors.New("provider exploded")
	sess, bus := turnEventSession(t, &fakeCompleter{err: boom})
	drain := collectTurnEvents(t, bus, 2)

	if _, err := sess.SendUser(context.Background(), "hello", nil); err == nil {
		t.Fatal("SendUser returned nil error; this test needs the failing path")
	}

	starts, ends, errs := partitionByKind(drain())
	if len(starts) != 1 {
		t.Fatalf("got %d turn_start events, want 1", len(starts))
	}
	if len(errs) != 1 {
		t.Fatalf("got %d error events, want exactly 1", len(errs))
	}
	if errs[0].Err == nil {
		t.Error("error terminal carried no Err")
	}
	if errs[0].TurnID != starts[0].TurnID {
		t.Errorf("error TurnID %q != start TurnID %q", errs[0].TurnID, starts[0].TurnID)
	}
	if len(ends) != 0 {
		t.Error("a failed turn published turn_end as well as error; a consumer treats both as terminals and would close the turn twice")
	}
}

// TestSessionWithoutABusDoesNotPanic keeps the publish optional. Most sessions
// in tests and in headless callers have no bus at all.
func TestSessionWithoutABusDoesNotPanic(t *testing.T) {
	sess := NewSession(&config.Resolved{Model: "m"}, &fakeCompleter{out: "reply"})
	if sess.EventBus != nil {
		t.Fatal("a fresh session should have no bus")
	}
	if _, err := sess.SendUser(context.Background(), "hello", nil); err != nil {
		t.Fatalf("SendUser: %v", err)
	}
}

// TestCancellationIsReportedAsCompletionNotFailure pins the classification the
// surfaces already use, so the event stream and the terminal output agree about
// what counts as "the user stopped this".
func TestCancellationIsReportedAsCompletionNotFailure(t *testing.T) {
	if !CancellationCanReplaceTurnError(nil) {
		t.Error("a nil error must be replaceable by a cancellation")
	}
	if !CancellationCanReplaceTurnError(context.Canceled) {
		t.Error("context.Canceled must be replaceable")
	}
	if !CancellationCanReplaceTurnError(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded must be replaceable")
	}
	if CancellationCanReplaceTurnError(errors.New("real failure")) {
		t.Error("a genuine failure must NOT be reported as a cancellation")
	}
}

// parseTurnEventID is the test's own reader for the published id, asserting it
// uses the prefix admission.go parses back out.
func parseTurnEventID(id string) (string, bool) {
	if len(id) <= len(turnIDPrefix) || id[:len(turnIDPrefix)] != turnIDPrefix {
		return "", false
	}
	return id[len(turnIDPrefix):], true
}
