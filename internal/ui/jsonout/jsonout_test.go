package jsonout

import (
	"bytes"
	"encoding/json"
	"errors"
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

// errAtWriter fails on its Nth Write call. Sweeping failAt proves Render
// aborts and propagates on a write failure at any point in the stream,
// not only the first.
type errAtWriter struct {
	failAt int
	calls  int
}

var errBoom = errors.New("boom")

func (w *errAtWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, errBoom
	}
	return len(p), nil
}

func TestRenderPropagatesWriteErrors(t *testing.T) {
	events := loadFixture(t)

	counter := &errAtWriter{failAt: -1}
	if err := Render(counter, events); err != nil {
		t.Fatalf("baseline render failed: %v", err)
	}
	total := counter.calls
	if total == 0 {
		t.Fatal("baseline render made no Write calls")
	}

	for n := 1; n <= total; n++ {
		w := &errAtWriter{failAt: n}
		if err := Render(w, events); err == nil {
			t.Errorf("expected Render to fail when write #%d of %d fails", n, total)
		}
	}
}
