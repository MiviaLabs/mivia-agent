package contextmgr

import (
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// The planner's source-range and idempotency-key refusals.
//
// A compaction plan is replayed by key: the same key must mean the same
// plan over the same events, or a retry applies a plan built from a
// different history. Every arm below is what stops a range or a key that
// would break that equality, and none of them fails loudly on its own -
// a bad range simply produces a plan over the wrong events.

func srcEvent(t *testing.T, session string, seq uint64) contextstate.SourceEvent {
	t.Helper()
	id, err := contextstate.NewSourceID(session, seq)
	if err != nil {
		t.Fatalf("NewSourceID(%s,%d): %v", session, seq, err)
	}
	return contextstate.SourceEvent{
		ID: id, Kind: "message", Role: "user",
		PayloadRef: "sha256:" + strings.Repeat("a", 64), Provenance: "host",
		RedactionStatus: "sanitized", Size: 4,
	}
}

// TestPlanSourceRangeRefusesANonContiguousEventRun: the range is derived
// from the first and last event, so a gap in the middle would produce a
// range that CLAIMS to cover events the plan never saw.
func TestPlanSourceRangeRefusesANonContiguousEventRun(t *testing.T) {
	for _, seqs := range [][]uint64{
		{1, 3}, // a gap
		{1, 1}, // a repeat
		{2, 1}, // out of order
	} {
		events := make([]contextstate.SourceEvent, 0, len(seqs))
		for _, s := range seqs {
			events = append(events, srcEvent(t, "session", s))
		}
		_, err := planSourceRange(PlanInput{SourceEvents: events})
		if err == nil {
			t.Errorf("sequence %v was accepted as contiguous", seqs)
			continue
		}
		if !strings.Contains(err.Error(), "not contiguous") {
			t.Errorf("sequence %v gave %q, want it to say the events are not contiguous", seqs, err)
		}
	}
}

// TestPlanSourceRangeRefusesEventsFromTwoSessions: a plan spans one
// session's log. Events from two would produce a range whose endpoints
// name different histories.
func TestPlanSourceRangeRefusesEventsFromTwoSessions(t *testing.T) {
	events := []contextstate.SourceEvent{srcEvent(t, "session-a", 1), srcEvent(t, "session-b", 2)}
	if _, err := planSourceRange(PlanInput{SourceEvents: events}); err == nil {
		t.Error("events from two sessions were accepted")
	}
}

// TestPlanSourceRangeRefusesAnInvalidEvent: each event is validated on
// the way past, so a malformed one cannot reach the fingerprint.
func TestPlanSourceRangeRefusesAnInvalidEvent(t *testing.T) {
	bad := srcEvent(t, "session", 1)
	bad.Kind = "" // required
	if _, err := planSourceRange(PlanInput{SourceEvents: []contextstate.SourceEvent{bad}}); err == nil {
		t.Error("an invalid source event was accepted")
	}
}

// TestAnExplicitRangeMustCoverTheEventsItIsGivenWith: when the caller
// supplies both, the range has to contain the events. A range narrower
// than the events would have the plan compact history it did not read.
func TestAnExplicitRangeMustCoverTheEventsItIsGivenWith(t *testing.T) {
	events := []contextstate.SourceEvent{srcEvent(t, "session", 5), srcEvent(t, "session", 6)}
	start, err := contextstate.NewSourceID("session", 6)
	if err != nil {
		t.Fatal(err)
	}
	end, err := contextstate.NewSourceID("session", 6)
	if err != nil {
		t.Fatal(err)
	}

	_, err = planSourceRange(PlanInput{
		SourceEvents: events,
		SourceRange:  contextstate.SourceRange{Start: start, End: end},
	})
	if err == nil {
		t.Fatal("a range starting after the first event was accepted")
	}
	if !strings.Contains(err.Error(), "does not cover") {
		t.Errorf("error %q does not say the range fails to cover the events", err)
	}
}

// TestAnExplicitRangeThatCoversTheEventsIsKept is the positive side: the
// caller's range wins when it is valid and wide enough, because the
// caller may be compacting a wider window than the events it passed.
func TestAnExplicitRangeThatCoversTheEventsIsKept(t *testing.T) {
	events := []contextstate.SourceEvent{srcEvent(t, "session", 5), srcEvent(t, "session", 6)}
	start, _ := contextstate.NewSourceID("session", 1)
	end, _ := contextstate.NewSourceID("session", 9)

	got, err := planSourceRange(PlanInput{
		SourceEvents: events,
		SourceRange:  contextstate.SourceRange{Start: start, End: end},
	})
	if err != nil {
		t.Fatalf("a covering range was refused: %v", err)
	}
	if got.Start.Sequence != 1 || got.End.Sequence != 9 {
		t.Errorf("range = %d..%d, want the caller's 1..9", got.Start.Sequence, got.End.Sequence)
	}
}

// TestASuppliedIdempotencyKeyIsValidatedNotTrusted: the key is the
// replay identity. A key with control characters or stray whitespace
// would round-trip differently through storage and logs, so two retries
// could disagree about whether they are the same plan.
func TestASuppliedIdempotencyKeyIsValidatedNotTrusted(t *testing.T) {
	for _, key := range []string{
		" leading",
		"trailing ",
		"has\x00null",
		"has\nnewline",
		strings.Repeat("k", contextstate.MaxIdentifierBytes+1),
	} {
		_, err := planIdempotencyKey(PlanInput{IdempotencyKey: key}, contextstate.SourceRange{}, 0, nil)
		if err == nil {
			t.Errorf("key %q was accepted", key)
		}
	}

	// A well-formed key is returned unchanged: the planner does not
	// rewrite what the caller will retry with.
	const good = "plan-2026-09-04-001"
	got, err := planIdempotencyKey(PlanInput{IdempotencyKey: good}, contextstate.SourceRange{}, 0, nil)
	if err != nil {
		t.Fatalf("a well-formed key was refused: %v", err)
	}
	if got != good {
		t.Errorf("key came back as %q, want %q unchanged", got, good)
	}
}
