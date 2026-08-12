package contextmgr

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

// TestTurnStateEmptySnapshotIsValid pins that a fresh tracker - and a nil one -
// snapshots to an empty, valid snapshot with no error.
func TestTurnStateEmptySnapshotIsValid(t *testing.T) {
	for name, tracker := range map[string]*TurnState{
		"zero value": {},
		"new":        NewTurnState(),
		"nil":        nil,
	} {
		snapshot, err := tracker.Snapshot()
		if err != nil {
			t.Fatalf("%s: empty snapshot rejected: %v", name, err)
		}
		if snapshot.State != "" || len(snapshot.Decisions) != 0 || len(snapshot.Evidence) != 0 ||
			len(snapshot.ChangedSurfaces) != 0 || len(snapshot.OpenWork) != 0 || len(snapshot.Risks) != 0 {
			t.Fatalf("%s: empty snapshot carries facts: %+v", name, snapshot)
		}
	}
}

// TestTurnStateRejectsItem33 pins the list cap on every accumulator list: the
// 33rd item is rejected and the tracker keeps exactly 32 items.
func TestTurnStateRejectsItem33(t *testing.T) {
	tracker := NewTurnState()
	for i := 0; i < MaxSummaryItems; i++ {
		item := strings.Repeat("a", 16) + string(rune('a'+i%26)) + string(rune('0'+i/26))
		if err := tracker.AddEvidence(item); err != nil {
			t.Fatalf("item %d rejected: %v", i+1, err)
		}
	}
	if err := tracker.AddEvidence("overflow"); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("item 33 error = %v, want ErrInvalidDTO", err)
	}
	snapshot, err := tracker.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Evidence) != MaxSummaryItems {
		t.Fatalf("evidence items = %d, want %d", len(snapshot.Evidence), MaxSummaryItems)
	}
}

// TestTurnStateListCapsAllLists drives the item-33 rejection through every
// accumulator list, not only evidence.
func TestTurnStateListCapsAllLists(t *testing.T) {
	adders := map[string]func(*TurnState, string) error{
		"decisions":        (*TurnState).AddDecision,
		"evidence":         (*TurnState).AddEvidence,
		"changed_surfaces": (*TurnState).AddChangedSurface,
		"open_work":        (*TurnState).AddOpenWork,
		"risks":            (*TurnState).AddRisk,
	}
	for name, add := range adders {
		t.Run(name, func(t *testing.T) {
			tracker := NewTurnState()
			for i := 0; i < MaxSummaryItems; i++ {
				if err := add(tracker, fmt.Sprintf("item-%d", i)); err != nil {
					t.Fatalf("item %d rejected: %v", i+1, err)
				}
			}
			if err := add(tracker, "overflow"); !errors.Is(err, contextstate.ErrInvalidDTO) {
				t.Fatalf("item 33 error = %v, want ErrInvalidDTO", err)
			}
		})
	}
}

// TestTurnStateRejectsInvalidItems pins the per-item validators shared with
// the summary envelope: oversized, duplicate, control-character, and invalid
// UTF-8 items are all rejected and never stored.
func TestTurnStateRejectsInvalidItems(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"oversized", strings.Repeat("x", MaxSummaryFieldBytes+1)},
		{"control char", "bad\x01value"},
		{"invalid utf8", "bad\xffvalue"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := NewTurnState()
			if err := tracker.AddRisk(tc.value); !errors.Is(err, contextstate.ErrInvalidDTO) {
				t.Fatalf("error = %v, want ErrInvalidDTO", err)
			}
			snapshot, err := tracker.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Risks) != 0 {
				t.Fatalf("rejected item was stored: %+v", snapshot.Risks)
			}
		})
	}

	tracker := NewTurnState()
	if err := tracker.AddDecision("same"); err != nil {
		t.Fatal(err)
	}
	if err := tracker.AddDecision("same"); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("duplicate error = %v, want ErrInvalidDTO", err)
	}
}

// TestTurnStateSetStateValidation pins SetState's envelope field validation:
// a control character is rejected while empty and ordinary text are accepted.
func TestTurnStateSetStateValidation(t *testing.T) {
	tracker := NewTurnState()
	if err := tracker.SetState(""); err != nil {
		t.Fatalf("empty state rejected: %v", err)
	}
	if err := tracker.SetState("latest assistant content"); err != nil {
		t.Fatalf("valid state rejected: %v", err)
	}
	if err := tracker.SetState("bad\x01state"); !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("control-char state error = %v, want ErrInvalidDTO", err)
	}
	snapshot, err := tracker.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "latest assistant content" {
		t.Fatalf("state = %q", snapshot.State)
	}
}

// TestTurnStateNilMethodsAreSafeNoOps pins that every accumulator method is a
// safe no-op on a nil tracker, so loop paths that may run without a tracker
// never panic.
func TestTurnStateNilMethodsAreSafeNoOps(t *testing.T) {
	var tracker *TurnState
	if err := tracker.SetState("state"); err != nil {
		t.Fatal(err)
	}
	for _, add := range []func() error{
		func() error { return tracker.AddDecision("d") },
		func() error { return tracker.AddEvidence("e") },
		func() error { return tracker.AddChangedSurface("s") },
		func() error { return tracker.AddOpenWork("w") },
		func() error { return tracker.AddRisk("r") },
	} {
		if err := add(); err != nil {
			t.Fatalf("nil tracker method errored: %v", err)
		}
	}
	if _, err := tracker.Snapshot(); err != nil {
		t.Fatalf("nil tracker snapshot errored: %v", err)
	}
}

// TestTurnStateSnapshotIsDefensive pins that mutating the returned snapshot
// slices never changes the tracker.
func TestTurnStateSnapshotIsDefensive(t *testing.T) {
	tracker := NewTurnState()
	if err := tracker.SetState("state"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"one", "two"} {
		if err := tracker.AddOpenWork(item); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := tracker.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.State = "mutated"
	snapshot.OpenWork[0] = "mutated"
	snapshot.OpenWork = append(snapshot.OpenWork, "extra")
	again, err := tracker.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if again.State != "state" || !reflect.DeepEqual(again.OpenWork, []string{"one", "two"}) {
		t.Fatalf("snapshot mutation leaked into the tracker: %+v", again)
	}
}
