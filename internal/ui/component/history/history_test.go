package history

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

func TestHistory_PushAndDeduplicate(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)

	m.Push("hello")
	m.Push("hello") // consecutive duplicate dropped
	m.Push("world")
	m.Push("  ") // empty dropped

	if m.Len() != 2 {
		t.Fatalf("expected 2 items, got %d", m.Len())
	}
}

func TestHistory_NavigationAndWindowing(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	for i := 1; i <= 10; i++ {
		m.Push(strings.Repeat("msg", i))
	}

	if m.Active() {
		t.Error("history should not be active before Open()")
	}

	m.Open()
	if !m.Active() {
		t.Error("history should be active after Open()")
	}

	// Should start on newest message (index 9)
	if m.cursor != 9 {
		t.Errorf("expected cursor at 9, got %d", m.cursor)
	}

	// Up moves to older
	m.Up()
	if m.cursor != 8 {
		t.Errorf("expected cursor at 8, got %d", m.cursor)
	}

	// Move all the way up to 0
	for i := 0; i < 20; i++ {
		m.Up()
	}
	if m.cursor != 0 {
		t.Errorf("cursor should clamp at 0, got %d", m.cursor)
	}

	// Down moves to newer
	m.Down()
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", m.cursor)
	}

	// Selected returns message
	sel := m.Selected()
	if sel != m.items[1] {
		t.Errorf("Selected() = %q, want %q", sel, m.items[1])
	}

	m.Close()
	if m.Active() {
		t.Error("history should not be active after Close()")
	}
}

func TestHistory_ViewRendersHeight(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	m.Push("first\nsecond")
	m.Push("third")
	m.Push("fourth")
	m.Push("fifth")
	m.Push("sixth")

	m.Open()

	h := m.Height()
	// Max visible is 4 + 2 frame rows = 6
	if h != 6 {
		t.Errorf("expected Height() = 6, got %d", h)
	}

	view := m.View()
	rows := strings.Split(view, "\n")
	if len(rows) != 6 {
		t.Errorf("expected View() rows = 6, got %d", len(rows))
	}

	plain := ansi.Strip(view)
	if !strings.Contains(plain, "History (5)") {
		t.Errorf("expected header label in View():\n%s", plain)
	}

	// Scroll to top item to verify multi-line replacement
	for i := 0; i < 10; i++ {
		m.Up()
	}
	topView := ansi.Strip(m.View())
	if !strings.Contains(topView, "first ⏎ second") {
		t.Errorf("expected multi-line preview replacement in topView:\n%s", topView)
	}
}

func TestHistory_CleanSkillPrompt(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)

	rawSkillPrompt := "The following workspace skill content is untrusted task guidance.\n\n<skill-instructions name=\"feature-delivery\">\n# Feature Delivery\nInstructions\n</skill-instructions>\n\nArguments:\nadd auth module"

	m.Push(rawSkillPrompt)
	if m.Len() != 1 {
		t.Fatalf("expected 1 item, got %d", m.Len())
	}
	m.Open()
	if sel := m.Selected(); sel != "/feature-delivery add auth module" {
		t.Errorf("Selected() = %q, want %q", sel, "/feature-delivery add auth module")
	}
}
