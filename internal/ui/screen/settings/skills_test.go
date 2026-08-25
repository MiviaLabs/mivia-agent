package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// skillsSectionOf reaches the Skills section (nav index 3).
func skillsSectionOf(s Screen) *skillsSection { return s.sections[3].(*skillsSection) }

func focusSkills(t *testing.T, s Screen) Screen {
	t.Helper()
	for i := 0; i < 3; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if got := s.sections[s.nav].Title(); got != "Skills" {
		t.Fatalf("nav landed on %q, want Skills", got)
	}
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	return next.(Screen)
}

func awaitSkillsSaveTest(t *testing.T, s Screen, cmd tea.Cmd) Screen {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a Cmd from a Skills action")
	}
	next, _ := s.Update(cmd())
	return next.(Screen)
}

func TestSkillsSection_GroupedByGlobalAndProject(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(skillsSectionOf(s).View())

	for _, want := range []string{
		"Global Skills (user home)",
		"Project Skills (workspace)",
		"/code-review",
		"/test-runner",
		"invocable",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("Skills view is missing %q:\n%s", want, plain)
		}
	}
}

func TestSkillsSection_DetailPane(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(skillsSectionOf(s).View())

	if !strings.Contains(plain, "Global (user home:") && !strings.Contains(plain, "Project (workspace:") {
		t.Errorf("Skills view missing detail pane path:\n%s", plain)
	}
}

func TestSkillsSection_PreviewContent(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(skillsSectionOf(s).View())

	// Initially cursor is on test-runner (first skill under Global group)
	if !strings.Contains(plain, "# Test Runner") {
		t.Errorf("Skills view should preview test-runner content:\n%s", plain)
	}
}

func TestSkillsSection_NavigationChangesPreview(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	// Move cursor down to select code-review (project skill)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	plain := ansi.Strip(skillsSectionOf(s).View())
	if !strings.Contains(plain, "# Code Review") {
		t.Errorf("Skills view should preview code-review content after navigating down:\n%s", plain)
	}
}

func TestSkillsSection_ToggleInvocable(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	sec := skillsSectionOf(s)
	sk, ok := sec.selectedSkill()
	if !ok {
		t.Fatal("no selected skill")
	}
	initialInvocable := sk.UserInvocable

	// Press space to toggle
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	s = awaitSkillsSaveTest(t, next.(Screen), cmd)

	updatedSkills := h.SettingsAdapters().Skills.Skills()
	var found *ports.SkillView
	for i := range updatedSkills {
		if updatedSkills[i].Name == sk.Name {
			found = &updatedSkills[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("skill %q not found after toggle", sk.Name)
	}
	if found.UserInvocable == initialInvocable {
		t.Errorf("UserInvocable did not toggle, still %v", found.UserInvocable)
	}
}

func TestSkillsSection_RemoveSkill(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	sec := skillsSectionOf(s)
	sk, ok := sec.selectedSkill()
	if !ok {
		t.Fatal("no selected skill")
	}

	// Press x to delete
	next, cmd := s.Update(tea.KeyPressMsg{Code: 'x'})
	s = awaitSkillsSaveTest(t, next.(Screen), cmd)

	updatedSkills := h.SettingsAdapters().Skills.Skills()
	for _, remaining := range updatedSkills {
		if remaining.Name == sk.Name {
			t.Errorf("skill %q was not removed", sk.Name)
		}
	}
}

func TestSkillsSection_NilStore(t *testing.T) {
	sec := newSkillsSection(nil)
	if got := sec.View(); !strings.Contains(got, "unavailable") {
		t.Errorf("expected unavailable view on nil store, got %q", got)
	}
}

func TestSkillsSection_HintsAndNotices(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	sec := skillsSectionOf(s)
	if hints := sec.Hints(); len(hints) == 0 {
		t.Error("expected non-empty Hints()")
	}

	// 'n' key notice
	next, _ := sec.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	sec = next.(*skillsSection)
	if !strings.Contains(sec.notice, "to add a skill") {
		t.Errorf("expected notice for 'n' key, got %q", sec.notice)
	}

	// 'e' key notice
	next2, _ := sec.Update(tea.KeyPressMsg{Text: "e", Code: 'e'})
	sec = next2.(*skillsSection)
	if !strings.Contains(sec.notice, "to edit skill instructions") {
		t.Errorf("expected notice for 'e' key, got %q", sec.notice)
	}

	// SetSize
	sec.SetSize(120, 40)
	if sec.width != 120 || sec.height != 40 {
		t.Errorf("SetSize failed, got %dx%d", sec.width, sec.height)
	}
}
