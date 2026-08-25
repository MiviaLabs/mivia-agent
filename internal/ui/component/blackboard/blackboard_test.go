package blackboard

import (
	"strings"
	"testing"

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

func TestBlackboardModel_EmptyState(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	if m.Active() {
		t.Fatal("expected inactive by default")
	}

	m.Open()
	if !m.Active() {
		t.Fatal("expected active after Open")
	}

	view := m.View()
	if !strings.Contains(view, "No findings posted yet") {
		t.Errorf("expected empty findings text in view, got %q", view)
	}

	m.ToggleTab()
	view = m.View()
	if !strings.Contains(view, "No inter-agent messages recorded") {
		t.Errorf("expected empty messages text in view, got %q", view)
	}
}

func TestBlackboardModel_FindingsAndNavigation(t *testing.T) {
	th := loadTheme(t)
	m := New(th, theme.TierTrueColor)
	m.SetWidth(80)

	m.AddFinding("researcher", "Finding 1: Mutex contention", nil)
	m.AddFinding("builder", "Finding 2: Syntax error in parser", nil)
	m.AddFinding("tester", "Finding 3: Test coverage regression", nil)

	if m.FindingsCount() != 3 {
		t.Fatalf("expected 3 findings, got %d", m.FindingsCount())
	}

	m.Open()
	view := m.View()
	if !strings.Contains(view, "Finding 1: Mutex contention") {
		t.Errorf("expected finding 1 in view, got %q", view)
	}

	// Move cursor down
	m.Down()
	m.Down()
	m.Up()

	// Switch to Messages tab and add message
	m.ToggleTab()
	m.AddMessage("orchestrator", "builder", "steer", "Focus on parser tests")
	if m.MessagesCount() != 1 {
		t.Fatalf("expected 1 message, got %d", m.MessagesCount())
	}

	view = m.View()
	if !strings.Contains(view, "Focus on parser tests") {
		t.Errorf("expected message body in view, got %q", view)
	}

	m.Close()
	if m.Active() {
		t.Fatal("expected inactive after Close")
	}
}
