package uievent

import (
	"strings"
	"testing"
)

func TestLoadFixture(t *testing.T) {
	raw := `[
		{"kind":"turn.start","turn_id":"t1","seq":1,"at":"2026-08-19T12:00:00Z","body":{"input":"hi"}},
		{"kind":"turn.end","turn_id":"t1","seq":2,"at":"2026-08-19T12:00:01Z","body":{"reason":"completed"}}
	]`
	events, err := LoadFixture(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Kind != KindTurnStart || events[1].Kind != KindTurnEnd {
		t.Errorf("unexpected kinds: %v, %v", events[0].Kind, events[1].Kind)
	}
}

func TestLoadFixtureMalformed(t *testing.T) {
	if _, err := LoadFixture(strings.NewReader("not json")); err == nil {
		t.Fatal("expected error for malformed fixture")
	}
}
