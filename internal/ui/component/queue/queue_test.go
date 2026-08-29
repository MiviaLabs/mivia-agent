package queue

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

func loadTheme(t *testing.T) theme.Theme {
	t.Helper()
	themes, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range themes {
		if th.Name == "mivia-dark" {
			return th
		}
	}
	t.Fatal("mivia-dark theme not found")
	return theme.Theme{}
}

func TestQueue_OpenAndNavigation(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	items := []string{"msg 1", "msg 2", "msg 3", "msg 4", "msg 5"}
	m.Open(items)

	if !m.Active() {
		t.Fatal("queue overlay should be active after Open()")
	}
	if m.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", m.cursor)
	}

	m.Down()
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", m.cursor)
	}

	m.Up()
	if m.cursor != 0 {
		t.Errorf("expected cursor at 0, got %d", m.cursor)
	}
}

func TestQueue_DeleteSelected(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	items := []string{"msg 1", "msg 2", "msg 3"}
	m.Open(items)

	m.Down() // select "msg 2"
	deleted, ok := m.DeleteSelected()
	if !ok || deleted != "msg 2" {
		t.Fatalf("expected deleted 'msg 2', got %q (ok=%v)", deleted, ok)
	}

	remaining := m.Items()
	if len(remaining) != 2 || remaining[0] != "msg 1" || remaining[1] != "msg 3" {
		t.Fatalf("unexpected remaining items: %v", remaining)
	}
}

func TestQueue_ViewEmpty(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	m.Open(nil)
	if m.Height() != 3 {
		t.Errorf("expected empty height 3, got %d", m.Height())
	}

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "No queued messages.") {
		t.Errorf("expected 'No queued messages.' in view:\n%s", view)
	}
}

// TestQueue_CursorEmpty pins the -1 sentinel: an empty queue has no
// selectable index, and callers must guard on it before indexing.
func TestQueue_CursorEmpty(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	if got := m.Cursor(); got != -1 {
		t.Errorf("Cursor() on empty model = %d, want -1", got)
	}

	m.Open(nil)
	if got := m.Cursor(); got != -1 {
		t.Errorf("Cursor() on opened-but-empty model = %d, want -1", got)
	}
}

// TestQueue_CursorNonEmpty pins that Cursor mirrors the selection once
// the queue holds items.
func TestQueue_CursorNonEmpty(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	m.Open([]string{"msg 1", "msg 2", "msg 3"})
	if got := m.Cursor(); got != 0 {
		t.Errorf("Cursor() = %d, want 0", got)
	}
	m.Down()
	if got := m.Cursor(); got != 1 {
		t.Errorf("Cursor() = %d, want 1", got)
	}
}

// TestQueue_InsertAtRoundTripsDeleteSelected pins InsertAt as the exact
// undo of DeleteSelected: deleting index 1 of 3 and then inserting the
// removed text back at 1 must reproduce the original slice, with the
// restored item left selected.
func TestQueue_InsertAtRoundTripsDeleteSelected(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	original := []string{"msg 1", "msg 2", "msg 3"}
	m.Open(append([]string(nil), original...))
	m.Down() // select "msg 2" at index 1

	deleted, ok := m.DeleteSelected()
	if !ok || deleted != "msg 2" {
		t.Fatalf("expected deleted 'msg 2', got %q (ok=%v)", deleted, ok)
	}

	m.InsertAt(1, deleted)

	got := m.Items()
	if len(got) != len(original) {
		t.Fatalf("got %v, want round-trip to %v", got, original)
	}
	for i := range original {
		if got[i] != original[i] {
			t.Fatalf("got %v, want round-trip to %v", got, original)
		}
	}
	if m.Cursor() != 1 {
		t.Errorf("Cursor() after InsertAt(1, ...) = %d, want 1 (restored item selected)", m.Cursor())
	}
}

// TestQueue_InsertAtClampsLow pins that a negative index clamps to the
// front of the slice rather than panicking.
func TestQueue_InsertAtClampsLow(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	m.Open([]string{"a", "b"})
	m.InsertAt(-1, "z")

	got := m.Items()
	want := []string{"z", "a", "b"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v, want %v", got, want)
	}
	if m.Cursor() != 0 {
		t.Errorf("Cursor() = %d, want 0 (clamped)", m.Cursor())
	}
}

// TestQueue_InsertAtClampsHigh pins that an index beyond the end clamps
// to append rather than panicking or being silently dropped.
func TestQueue_InsertAtClampsHigh(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	m.Open([]string{"a", "b"})
	m.InsertAt(99, "z")

	got := m.Items()
	want := []string{"a", "b", "z"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %v, want %v", got, want)
	}
	if m.Cursor() != 2 {
		t.Errorf("Cursor() = %d, want 2 (clamped)", m.Cursor())
	}
}

// TestQueue_ViewShowsForceHint pins that both the unicode and ASCII/NoTTY
// hint variants advertise the force-send key. Width is generous (120)
// because the label is long enough that HintFits' narrow fallback would
// otherwise drop it - the fallback itself is untouched by this change.
func TestQueue_ViewShowsForceHint(t *testing.T) {
	th := loadTheme(t)

	m := New(th, theme.TierTrueColor)
	m.SetWidth(120)
	m.Open([]string{"msg 1"})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "f: force") {
		t.Errorf("expected 'f: force' in truecolor view:\n%s", view)
	}

	mAscii := New(th, theme.TierASCII)
	mAscii.SetWidth(120)
	mAscii.Open([]string{"msg 1"})
	if view := ansi.Strip(mAscii.View()); !strings.Contains(view, "f: force") {
		t.Errorf("expected 'f: force' in ASCII view:\n%s", view)
	}
}
