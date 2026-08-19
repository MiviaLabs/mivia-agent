package jsonout

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/ui/stream"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func loadFixture(t *testing.T) []uievent.Event {
	t.Helper()
	events, err := stream.DefaultFixture()
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// TestRenderRoundTrips proves the property that matters for a --output
// json consumer: every rendered line decodes back to the exact event it
// came from.
func TestRenderRoundTrips(t *testing.T) {
	events := loadFixture(t)
	var buf bytes.Buffer
	if err := Render(&buf, events); err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(&buf)
	var got []uievent.Event
	for dec.More() {
		var ev uievent.Event
		if err := dec.Decode(&ev); err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	if len(got) != len(events) {
		t.Fatalf("got %d events, want %d", len(got), len(events))
	}
	for i := range events {
		wantJSON, err := json.Marshal(events[i])
		if err != nil {
			t.Fatal(err)
		}
		gotJSON, err := json.Marshal(got[i])
		if err != nil {
			t.Fatal(err)
		}
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("event %d round-trip mismatch:\n got  %s\n want %s", i, gotJSON, wantJSON)
		}
	}
}

func TestRenderOneLinePerEvent(t *testing.T) {
	events := loadFixture(t)
	var buf bytes.Buffer
	if err := Render(&buf, events); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Count(buf.Bytes(), []byte("\n"))
	if lines != len(events) {
		t.Errorf("got %d lines, want %d (one per event)", lines, len(events))
	}
}

func TestRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty event slice, got %q", buf.String())
	}
}
