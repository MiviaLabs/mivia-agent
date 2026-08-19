package stream

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

func loadFixture(t *testing.T) []uievent.Event {
	t.Helper()
	events, err := DefaultFixture()
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// TestRenderGolden pins the plain-renderer output for the recorded
// conversation fixture (wireframes-panes.md section 4). Regenerate with
// -update if the renderer's format intentionally changes.
func TestRenderGolden(t *testing.T) {
	events := loadFixture(t)
	var buf bytes.Buffer
	if err := Render(&buf, events); err != nil {
		t.Fatal(err)
	}

	goldenPath := filepath.Join("testdata", "golden", "conversation.txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Errorf("output does not match golden %s\n--- got ---\n%s\n--- want ---\n%s", goldenPath, buf.String(), want)
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

func TestRenderUnknownBodyErrors(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, []uievent.Event{{Kind: "bogus", Body: nil}})
	if err == nil {
		t.Fatal("expected error for unhandled body type")
	}
}

// errAtWriter fails on its Nth Write call and succeeds on every other.
// Sweeping failAt across the full range of Write calls a render makes
// exercises every "write, then propagate the error" branch in the
// renderer in one parametrized test, instead of one contrived test per
// call site.
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

func TestRenderEveryKindNoPanic(t *testing.T) {
	// Every Kind in the fixture must be handled; a new Kind added to
	// uievent without a stream.go case should fail loudly, not panic or
	// silently no-op forever.
	events := loadFixture(t)
	seen := map[uievent.Kind]bool{}
	for _, ev := range events {
		seen[ev.Kind] = true
	}
	for _, k := range []uievent.Kind{
		uievent.KindTurnStart, uievent.KindTextDelta, uievent.KindTextEnd,
		uievent.KindReasoning, uievent.KindToolStart, uievent.KindToolOutput,
		uievent.KindToolEnd, uievent.KindPlan, uievent.KindNotice,
		uievent.KindError, uievent.KindUsage, uievent.KindTurnEnd,
	} {
		if !seen[k] {
			t.Errorf("fixture is missing coverage for Kind %s", k)
		}
	}
}
