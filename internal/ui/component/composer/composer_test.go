package composer

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestNewIsEmptyAndFocused(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	if got := m.Value(); got != "" {
		t.Errorf("got %q, want empty value on a new composer", got)
	}
}

func TestUpdateTypesText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	next, _ = next.Update(tea.KeyPressMsg{Text: "i", Code: 'i'})
	if got := next.Value(); got != "hi" {
		t.Errorf("got %q, want \"hi\"", got)
	}
}

func TestClearResetsValue(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	next.Clear()
	if got := next.Value(); got != "" {
		t.Errorf("got %q, want empty after Clear", got)
	}
}

func TestViewShowsPromptAndText(t *testing.T) {
	m := New(loadTheme(t), theme.TierASCII, 40)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	got := next.View()
	// RoleAccent is bold, and bold survives even the ASCII tier (by
	// design - see render.TestRoleBoldSurvivesNoColour), so the prompt
	// arrives wrapped in an SGR bold sequence rather than as a literal
	// prefix.
	if !strings.Contains(got, "> ") {
		t.Errorf("got %q, want the accent prompt \"> \" present", got)
	}
	if !strings.Contains(got, "h") {
		t.Errorf("got %q, want typed text present", got)
	}
}

func TestSetWidthClampsBelowPrompt(t *testing.T) {
	// A terminal narrower than the prompt itself must not produce a
	// zero or negative input width (bubbles' textinput would render
	// nothing, or panic on some paths).
	m := New(loadTheme(t), theme.TierASCII, 40)
	m.SetWidth(1)
	next, _ := m.Update(tea.KeyPressMsg{Text: "h", Code: 'h'})
	if got := next.Value(); got != "h" {
		t.Errorf("got %q, want the composer still usable at a clamped width", got)
	}
}

// widthOf measures the display width of one rendered row.
func widthOf(row string) int { return ansi.StringWidth(row) }

// sizedComposer builds a composer at width with optional typed text.
func sizedComposer(t *testing.T, width int, text string) Model {
	t.Helper()
	m := New(loadTheme(t), theme.TierASCII, width)
	if text != "" {
		m.SetValue(text)
	}
	return m
}

// TestComposerViewIsExactlyTheWidth is the regression test for the
// measured sizing bug: textinput.SetWidth(n) renders n+1 display columns
// (one cell is reserved for the cursor), so the composer must subtract
// that cell when it sizes the input.
//
// The input line is measured BEFORE View's clamp, on inputLine(). The
// clamped View would pass even with the old bug, because the clamp
// absorbs the overflow - exactly the silent regression this test exists
// to catch. Menu rows are only checked against the clamped View: they
// may be shorter than the width, never wider.
func TestComposerViewIsExactlyTheWidth(t *testing.T) {
	texts := []string{"", "hi", "a longer reply that still fits"}
	for width := 4; width <= 100; width++ {
		for _, text := range texts {
			m := sizedComposer(t, width, text)
			if got := widthOf(m.inputLine()); got != width {
				t.Errorf("width %d text %q: input line is %d columns, want exactly %d",
					width, text, got, width)
			}
			for _, row := range strings.Split(m.View(), "\n") {
				if got := widthOf(row); got != width {
					t.Errorf("width %d text %q: view row is %d columns, want exactly %d",
						width, text, got, width)
				}
			}

			// Same proof with the completion menu open above the line.
			withMenu := m
			withMenu.SetCommands([]Command{
				{Name: "agent", Desc: "pick the agent for this turn - a description long enough to need clipping"},
				{Name: "clear", Desc: "clear the transcript"},
			})
			withMenu.SetValue("/a")
			if !withMenu.MenuActive() {
				t.Fatalf("width %d: expected the menu open for \"/a\"", width)
			}
			if got := widthOf(withMenu.inputLine()); got != width {
				t.Errorf("width %d menu open: input line is %d columns, want exactly %d",
					width, got, width)
			}
			menuRows := strings.Split(withMenu.View(), "\n")
			for _, row := range menuRows {
				if got := widthOf(row); got > width {
					t.Errorf("width %d menu open: row is %d columns, want at most %d (%q)",
						width, got, width, ansi.Strip(row))
				}
			}
			if got := widthOf(menuRows[len(menuRows)-1]); got != width {
				t.Errorf("width %d menu open: input row is %d columns, want exactly %d",
					width, got, width)
			}
		}
	}
}

// TestComposerClampNeverFiresUnderNormalSizing proves the ansi.Truncate
// in View is a backstop only. The line is measured BEFORE the clamp; for
// any width that can hold the prompt, the cursor cell and one text
// cell, the measured line must already equal the width. A backstop that
// silently absorbs a sizing regression is worse than none.
func TestComposerClampNeverFiresUnderNormalSizing(t *testing.T) {
	for width := 4; width <= 100; width++ {
		for _, text := range []string{"", "hi", "reply text"} {
			m := sizedComposer(t, width, text)
			if got := widthOf(m.inputLine()); got != width {
				t.Errorf("width %d text %q: pre-clamp line is %d columns, want %d (the clamp is hiding a sizing bug)",
					width, text, got, width)
			}
		}
	}
}

// TestComposerTinyWidthsNeverPanicOrOverflow guards the degenerate range
// where prompt plus cursor cell no longer fit: no panic, and the final
// clamped view never exceeds the width.
func TestComposerTinyWidthsNeverPanicOrOverflow(t *testing.T) {
	for width := 1; width <= 3; width++ {
		m := sizedComposer(t, width, "way longer than the width")
		m.SetCommands([]Command{{Name: "agent", Desc: "pick the agent"}})
		m.SetValue("/a")
		var rows []string
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("width %d: View panicked: %v", width, r)
				}
			}()
			rows = strings.Split(m.View(), "\n")
		}()
		for _, row := range rows {
			if got := widthOf(row); got > width {
				t.Errorf("width %d: row is %d columns, want at most %d (%q)", width, got, width, ansi.Strip(row))
			}
		}
	}
}

// TestComposerLongTextStaysWithinWidth covers the scrolled-value case:
// when the typed text is longer than the field, the input's visible
// window plus the clamp must keep the row inside the width.
func TestComposerLongTextStaysWithinWidth(t *testing.T) {
	for width := 4; width <= 100; width++ {
		m := sizedComposer(t, width, strings.Repeat("x", width*3))
		for _, row := range strings.Split(m.View(), "\n") {
			if got := widthOf(row); got > width {
				t.Errorf("width %d: long-text row is %d columns, want at most %d", width, got, width)
			}
		}
	}
}
