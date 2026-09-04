package composer

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// The popup's own width arithmetic.
//
// The popup is an overlay the owning screen draws over the transcript rows
// above the bar. It reserves no row of its own, so every row it returns is
// painted straight onto the transcript: a row narrower than PopupWidth()
// leaves transcript text showing through the menu, and a wider one paints
// over a column the screen never allocated. Neither fails a test on its
// own - the menu simply looks wrong on a narrow pane.

// popupModel is a composer with the command menu open at width w.
func popupModel(t *testing.T, w int) Model {
	t.Helper()
	m := New(theme.Theme{Name: "test"}, theme.TierASCII, w)
	m.SetCommands([]Command{{Name: "compact", Desc: "compact the context"}})
	m.SetValue("/comp")
	if !m.MenuActive() {
		t.Fatalf("width %d: the command menu did not open", w)
	}
	return m
}

// TestNoPopupIsDrawnWhenThereIsNoRoomForItsPadding: each row carries one
// column of padding on each side, so a popup narrower than three columns
// has no interior left. Drawing one anyway would mean negative-width
// padding arithmetic on a row that could show nothing.
func TestNoPopupIsDrawnWhenThereIsNoRoomForItsPadding(t *testing.T) {
	for _, w := range []int{1, 2} {
		m := popupModel(t, w)
		if m.PopupWidth() > 2 {
			t.Fatalf("fixture broken: width %d gives popup width %d", w, m.PopupWidth())
		}
		if rows := m.Popup(); rows != nil {
			t.Errorf("width %d: drew a %d-row popup with no interior: %q", w, len(rows), rows)
		}
	}
	// Three columns is the first width with an interior, and it draws.
	if rows := popupModel(t, 3).Popup(); len(rows) == 0 {
		t.Error("width 3: the first drawable popup width drew nothing")
	}
}

// TestEveryPopupRowIsExactlyPopupWidth exercises all three arms of the row
// fit - pad, truncate, and exact - and asserts the one invariant they
// share. Width 37 is the exact-fit boundary for this fixture: the menu
// line is 31 columns and the interior is 31, so the row is used verbatim.
func TestEveryPopupRowIsExactlyPopupWidth(t *testing.T) {
	for _, w := range []int{20, 37, 40} {
		m := popupModel(t, w)
		want := m.PopupWidth()
		for i, row := range m.Popup() {
			if got := ansi.StringWidth(ansi.Strip(row)); got != want {
				t.Errorf("width %d: popup row %d is %d columns, want %d: %q",
					w, i, got, want, ansi.Strip(row))
			}
		}
	}
}

// TestAPopupRowTooWideForTheInteriorIsTruncatedNotWrapped: an over-wide
// item must lose its tail, not spill onto a second row. A wrapped row
// would shift every row below it and paint over one more transcript line
// than the screen reserved.
func TestAPopupRowTooWideForTheInteriorIsTruncatedNotWrapped(t *testing.T) {
	const w = 20
	m := popupModel(t, w)
	inner := m.PopupWidth() - 2
	full := ansi.Strip(strings.Split(m.activeMenuView(), "\n")[0])
	if ansi.StringWidth(full) <= inner {
		t.Fatalf("fixture broken: the menu line (%d cols) already fits the interior (%d)", ansi.StringWidth(full), inner)
	}

	rows := m.Popup()
	if len(rows) < 2 {
		t.Fatalf("popup has %d rows; want a pad row plus at least one item", len(rows))
	}
	item := ansi.Strip(rows[1])
	body := strings.TrimSuffix(strings.TrimPrefix(item, " "), " ")
	if ansi.StringWidth(body) != inner {
		t.Fatalf("truncated item body is %d columns, want the interior width %d: %q", ansi.StringWidth(body), inner, body)
	}
	if !strings.HasPrefix(full, body) {
		t.Fatalf("truncated item %q is not a prefix of the menu line %q", body, full)
	}
}

// TestAPopupRowThatExactlyFitsIsUsedVerbatim pins the third arm: a line
// whose width equals the interior must be neither padded nor cut. Padding
// it would overflow the row and truncating it would drop its last column.
func TestAPopupRowThatExactlyFitsIsUsedVerbatim(t *testing.T) {
	const w = 37
	m := popupModel(t, w)
	inner := m.PopupWidth() - 2
	full := ansi.Strip(strings.Split(m.activeMenuView(), "\n")[0])
	if ansi.StringWidth(full) != inner {
		t.Fatalf("fixture broken: menu line is %d columns, interior is %d; width %d is no longer the exact-fit boundary",
			ansi.StringWidth(full), inner, w)
	}

	item := ansi.Strip(m.Popup()[1])
	if want := " " + full + " "; item != want {
		t.Fatalf("exact-fit item = %q; want the menu line verbatim between its padding columns %q", item, want)
	}
}

// TestPopupOffsetIsZeroOnAnUnpaddedBar: the offset exists to line the
// popup up with the bar's left padding. A bar too narrow to afford that
// padding has none, so an offset would push the popup off the bar it
// belongs to.
func TestPopupOffsetIsZeroOnAnUnpaddedBar(t *testing.T) {
	for _, w := range []int{1, 3, 6} {
		m := popupModel(t, w)
		if m.Padded() {
			t.Fatalf("fixture broken: width %d still claims padding", w)
		}
		if got := m.PopupOffset(); got != 0 {
			t.Errorf("width %d: unpadded bar offsets its popup by %d columns", w, got)
		}
		if got := m.PopupWidth(); got != w {
			t.Errorf("width %d: unpadded bar reserved an inset anyway (popup width %d)", w, got)
		}
	}
	// A wide bar does offset, so the zero above is the narrow-case arm and
	// not a broken accessor.
	if got := popupModel(t, 60).PopupOffset(); got <= 0 {
		t.Errorf("a padded bar returned offset %d; want its left padding", got)
	}
}

// TestAnIdleComposerDrawsNoHint: the footer hint names the keys of the
// menu that is open. With no menu open there are no such keys, and the
// placeholder already names "/" for commands, so a hint here would be
// stale advice drawn over the transcript.
func TestAnIdleComposerDrawsNoHint(t *testing.T) {
	idle := New(theme.Theme{Name: "test"}, theme.TierASCII, 60)
	if idle.MenuActive() || idle.MentionMenuActive() {
		t.Fatal("fixture broken: a fresh composer already has a menu open")
	}
	if got := idle.menuHint(); got != "" {
		t.Errorf("idle composer hint = %q; want none", got)
	}
	// A composer with a menu open does carry one, so the empty string above
	// is the idle arm rather than a hint that never renders.
	if got := popupModel(t, 60).menuHint(); got == "" {
		t.Error("an open command menu drew no key hint")
	}
}
