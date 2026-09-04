package composer

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
)

// The composer's narrow-terminal degradation.
//
// Every arm here is what the composer does when there is not enough
// width to draw what it would prefer. They are unreachable on a normal
// terminal and reached constantly on a split pane, and none of them
// fails when it breaks - the composer simply draws wider than its column
// and the layout around it shifts, which is the one thing ux-rules 2.8
// says must never happen to this component.

func narrow(t *testing.T, width int) Model {
	t.Helper()
	m := New(theme.Theme{Name: "test"}, theme.TierTrueColor, width)
	return m
}

// TestTheComposerDropsItsPaddingWhenTooNarrowToAfford it: the padding
// occupies real cells, so below the threshold it is dropped rather than
// eating the input's own columns.
func TestTheComposerDropsItsPaddingWhenTooNarrowToAffordIt(t *testing.T) {
	if narrow(t, 4).Padded() {
		t.Error("a 4-column composer claims padding it cannot afford")
	}
	if !narrow(t, 80).Padded() {
		t.Error("an 80-column composer dropped its padding")
	}

	// The padding decision and the width it leaves for content must
	// agree: a composer that says it is unpadded but still subtracts the
	// inset draws two columns narrower than it reports.
	for _, w := range []int{1, 4, 8, 20, 80} {
		m := narrow(t, w)
		popup := m.PopupWidth()
		if popup > w {
			t.Errorf("width %d: popup width %d exceeds the composer", w, popup)
		}
		if m.Padded() && popup >= w {
			t.Errorf("width %d: padded composer did not reserve its inset (popup=%d)", w, popup)
		}
		if !m.Padded() && popup != w {
			t.Errorf("width %d: unpadded composer reserved an inset anyway (popup=%d)", w, popup)
		}
	}
}

// TestTheComposerNeverDrawsWiderThanItsColumn is the rule the arms above
// exist for. It is asserted on the rendered output rather than on the
// arithmetic, because the arithmetic is what is under test.
func TestTheComposerNeverDrawsWiderThanItsColumn(t *testing.T) {
	for _, w := range []int{1, 2, 4, 8, 12, 20, 40, 100} {
		m := narrow(t, w)
		m.SetValue("a value long enough to need wrapping at any of these widths")
		for i, row := range strings.Split(m.View(), "\n") {
			if got := ansi.StringWidth(ansi.Strip(row)); got > w {
				t.Errorf("width %d: row %d is %d columns: %q", w, i, got, ansi.Strip(row))
			}
		}
	}
}

// TestTheCommandMarkIsSkippedWhenThereIsNoRoomForIt: the mark restyles a
// span of the first row, so a composer too narrow to hold the prompt plus
// the token has no span to restyle. Marking anyway would index past the
// row.
func TestTheCommandMarkIsSkippedWhenThereIsNoRoomForIt(t *testing.T) {
	for _, w := range []int{1, 2, 3, 4, 6} {
		m := New(theme.Theme{Name: "test"}, theme.TierTrueColor, w)
		m.SetCommands([]Command{{Name: "compact", Desc: "compact"}})
		m.SetValue("/compact")
		_ = m.View() // must not panic

		if got := m.markCommandToken("", 8); got != "" {
			t.Errorf("width %d: marking an empty body produced %q", w, got)
		}
		// A zero-width token has no span, so the body comes back untouched.
		body := "› /compact"
		if got := m.markCommandToken(body, 0); got != body {
			t.Errorf("width %d: a zero-width mark rewrote the body", w)
		}
	}
}

// TestThePopupHintDegradesToNothingRatherThanOverflow: the hint is an
// affordance, not content, so it gives way. A hint that stayed at full
// length would push the popup past the pane it is drawn inside.
func TestThePopupHintDegradesToNothingRatherThanOverflow(t *testing.T) {
	full := -1
	for _, w := range []int{100, 60, 40, 30, 20, 12, 6, 2} {
		m := New(theme.Theme{Name: "test"}, theme.TierTrueColor, w)
		m.SetCommands([]Command{{Name: "compact", Desc: "compact the context"}})
		m.SetValue("/")

		hint := ansi.Strip(m.menuHint())
		if hint != "" && ansi.StringWidth(hint) > m.PopupWidth() {
			t.Errorf("width %d: hint %q is wider than the popup (%d)", w, hint, m.PopupWidth())
		}
		if w == 100 {
			full = ansi.StringWidth(hint)
		}
		if w == 2 && hint != "" {
			t.Errorf("width 2 still drew a hint: %q", hint)
		}
	}
	if full <= 0 {
		t.Error("a wide composer drew no hint at all; the degradation tests above prove nothing")
	}
}
