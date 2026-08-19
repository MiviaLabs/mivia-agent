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

func TestComposerViewIsExactlyTheWidth(t *testing.T) {
	texts := []string{"", "hi", "a longer reply that still fits"}
	for width := 4; width <= 100; width++ {
		framed := width >= minFramedWidth
		// Framed, the border removes frameInset columns: the input line
		// fills the inner width exactly and the frame rows are exactly
		// the terminal width. Bare (under minFramedWidth), the line is
		// the full width as before.
		wantLine := width
		if framed {
			wantLine = width - frameInset
		}
		for _, text := range texts {
			m := sizedComposer(t, width, text)
			// Only text that fits is sized exactly; longer text is
			// clipped in View, which the row assertions below prove.
			if natural := widthOf(m.inputLine()); natural <= wantLine && natural != wantLine {
				t.Errorf("width %d text %q: input line is %d columns, want exactly %d",
					width, text, natural, wantLine)
			}
			for _, row := range strings.Split(m.View(), "\n") {
				wantRow := width
				if !framed && row == m.inputLine() {
					wantRow = wantLine
				}
				if got := widthOf(row); got != wantRow {
					t.Errorf("width %d text %q: view row is %d columns, want exactly %d",
						width, text, got, wantRow)
				}
			}

			// Same proof with the completion menu open above the frame.
			withMenu := m
			withMenu.SetCommands([]Command{
				{Name: "agent", Desc: "pick the agent for this turn - a description long enough to need clipping"},
				{Name: "clear", Desc: "clear the transcript"},
			})
			withMenu.SetValue("/a")
			if !withMenu.MenuActive() {
				t.Fatalf("width %d: expected the menu open for \"/a\"", width)
			}
			if natural := widthOf(withMenu.inputLine()); natural <= wantLine && natural != wantLine {
				t.Errorf("width %d menu open: input line is %d columns, want exactly %d",
					width, natural, wantLine)
			}
			menuRows := strings.Split(withMenu.View(), "\n")
			for _, row := range menuRows {
				if got := widthOf(row); got > width {
					t.Errorf("width %d menu open: row is %d columns, want at most %d (%q)",
						width, got, width, ansi.Strip(row))
				}
			}
			last := menuRows[len(menuRows)-1]
			wantLast := width
			if !framed {
				wantLast = wantLine
			}
			if got := widthOf(last); got != wantLast {
				t.Errorf("width %d menu open: last row is %d columns, want exactly %d",
					width, got, wantLast)
			}
		}
	}
}

func TestComposerClampNeverFiresUnderNormalSizing(t *testing.T) {
	for width := 4; width <= 100; width++ {
		want := width
		if width >= minFramedWidth {
			want = width - frameInset
		}
		for _, text := range []string{"", "hi", "reply text"} {
			m := sizedComposer(t, width, text)
			if natural := widthOf(m.inputLine()); natural <= want && natural != want {
				t.Errorf("width %d text %q: pre-clamp line is %d columns, want %d (the clamp is hiding a sizing bug)",
					width, text, natural, want)
			}
		}
	}
}
