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

func TestSkillsSection_RemoveSkill_RequiresConfirmation(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	sec := skillsSectionOf(s)
	target, ok := sec.selectedSkill()
	if !ok {
		t.Fatal("no skill selected")
	}

	// 1. First 'x' should show confirmation notice and NOT delete yet
	next, cmd := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if cmd != nil {
		t.Fatal("expected no immediate save cmd on first 'x' (must prompt for confirmation)")
	}
	s = next.(Screen)
	plain := ansi.Strip(skillsSectionOf(s).View())
	if !strings.Contains(plain, "Delete skill \""+target.Name+"\"? Press 'x' or 'y' to confirm") {
		t.Fatalf("expected delete confirmation notice on first 'x', got:\n%s", plain)
	}

	// 2. Press 'esc' to cancel deletion
	next, _ = s.Update(tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc})
	s = next.(Screen)
	plain = ansi.Strip(skillsSectionOf(s).View())
	if strings.Contains(plain, "Delete skill") {
		t.Errorf("expected confirmation prompt to be cleared after esc, got:\n%s", plain)
	}

	// Verify skill still exists in store
	found := false
	for _, sk := range h.SettingsAdapters().Skills.Skills() {
		if sk.Name == target.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("skill was removed prematurely after esc")
	}

	// 3. Press 'x' again, then confirm with second 'x'
	next, _ = s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = next.(Screen)
	next, cmd = s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = awaitSkillsSaveTest(t, next.(Screen), cmd)

	for _, sk := range h.SettingsAdapters().Skills.Skills() {
		if sk.Name == target.Name {
			t.Errorf("skill %q still present after confirmed removal", target.Name)
		}
	}
}

func TestSkillsSection_DeleteConfirmation_AnyKeyCancelsWithoutSideEffect(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	sec := skillsSectionOf(s)
	initialCursor := sec.cursor

	// Press 'x' to trigger confirmation
	next, _ := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = next.(Screen)
	sec = skillsSectionOf(s)
	if sec.confirmDeleteName == "" {
		t.Fatal("expected confirmDeleteName to be set")
	}

	// Press 'j' (down navigation) - should cancel confirmation without changing cursor
	next, _ = s.Update(tea.KeyPressMsg{Text: "j", Code: 'j'})
	s = next.(Screen)
	sec = skillsSectionOf(s)
	if sec.confirmDeleteName != "" {
		t.Errorf("expected confirmDeleteName to be cleared")
	}
	if sec.cursor != initialCursor {
		t.Errorf("cursor changed on cancelled delete, got %d want %d", sec.cursor, initialCursor)
	}
}

func typeText(s Screen, text string) Screen {
	for _, ch := range text {
		next, _ := s.Update(tea.KeyPressMsg{Text: string(ch), Code: ch})
		s = next.(Screen)
	}
	return s
}

func TestAddingASkill_FormAndPersist(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	// Press 'n' to open Add form
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)
	sec := skillsSectionOf(s)
	if !sec.editing {
		t.Fatal("expected section to be in editing mode after 'n'")
	}
	if plain := ansi.Strip(sec.View()); !strings.Contains(plain, "Add New Skill") {
		t.Fatalf("expected Add New Skill header in view, got:\n%s", plain)
	}

	s = typeText(s, "custom-lint")

	// Move down through form fields and fill them
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	next, _ = s.Update(down)
	s = typeText(next.(Screen), "runs custom project linter")
	next, _ = s.Update(down) // Invocable
	next, _ = next.(Screen).Update(down)
	s = typeText(next.(Screen), "read_file, run_command")
	next, _ = s.Update(down)
	s = typeText(next.(Screen), "lint, check code")
	next, _ = s.Update(down)
	s = typeText(next.(Screen), "# Custom Lint")

	// Press ctrl+s to save
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	s = awaitSkillsSaveTest(t, next.(Screen), cmd)

	// Verify new skill was created and persisted in store
	var created *ports.SkillView
	for _, sk := range h.SettingsAdapters().Skills.Skills() {
		if sk.Name == "custom-lint" {
			skCopy := sk
			created = &skCopy
			break
		}
	}
	if created == nil {
		t.Fatal("expected new skill 'custom-lint' in store, but not found")
	}
	if created.Description != "runs custom project linter" {
		t.Errorf("got description %q, want 'runs custom project linter'", created.Description)
	}
	if len(created.Tools) != 2 || created.Tools[0] != "read_file" || created.Tools[1] != "run_command" {
		t.Errorf("got tools %v, want ['read_file', 'run_command']", created.Tools)
	}
	if len(created.Triggers) != 2 || created.Triggers[0] != "lint" || created.Triggers[1] != "check code" {
		t.Errorf("got triggers %v, want ['lint', 'check code']", created.Triggers)
	}
}

func TestEditingASkill_FormAndPersist(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	// Press 'enter' on selected skill (test-runner)
	next, _ := s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	s = next.(Screen)
	sec := skillsSectionOf(s)
	if !sec.editing {
		t.Fatal("expected section to be in editing mode after 'enter'")
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Edit Skill: test-runner") {
		t.Fatalf("expected Edit Skill header in view, got:\n%s", plain)
	}

	// Cursor starts on Description field.
	// Move down to Tools field (Description -> Invocable -> Tools)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	// Press ctrl+s to save
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	s = awaitSkillsSaveTest(t, next.(Screen), cmd)

	sec = skillsSectionOf(s)
	if sec.editing {
		t.Error("expected section to exit editing mode after successful save")
	}

	// Verify skill still exists in store
	var updated *ports.SkillView
	for _, sk := range h.SettingsAdapters().Skills.Skills() {
		if sk.Name == "test-runner" {
			skCopy := sk
			updated = &skCopy
			break
		}
	}
	if updated == nil {
		t.Fatal("skill 'test-runner' missing from store after edit")
	}
}

func TestRenamingASkill_FormAndPersist(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	// Move cursor to code-review
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	// Press 'enter' to edit
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Move up to Name field
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	s = next.(Screen)

	// Clear and change name
	sec := skillsSectionOf(s)
	sec.formFields[skillFormName].SetValue("code-review-v2")

	// Press ctrl+s to save
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	s = awaitSkillsSaveTest(t, next.(Screen), cmd)

	// Verify old name is gone, new name is present
	foundOld := false
	foundNew := false
	for _, sk := range h.SettingsAdapters().Skills.Skills() {
		if sk.Name == "code-review" {
			foundOld = true
		}
		if sk.Name == "code-review-v2" {
			foundNew = true
		}
	}
	if foundOld {
		t.Error("expected old skill 'code-review' to be removed on rename")
	}
	if !foundNew {
		t.Error("expected new skill 'code-review-v2' to exist after rename")
	}
}

func TestSkillsSection_DuplicateNameValidation(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	// Press 'n' to add skill
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)

	// Type existing project skill name: "code-review"
	sec := skillsSectionOf(s)
	sec.formFields[skillFormName].SetValue("code-review")

	// Press ctrl+s
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("expected no save cmd when duplicate name is entered")
	}
	s = next.(Screen)
	plain := ansi.Strip(skillsSectionOf(s).View())
	if !strings.Contains(plain, "already exists") {
		t.Errorf("expected duplicate name warning in view, got:\n%s", plain)
	}
}

func TestSkillsSection_FormNavigation(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	// Press 'n' to open Add form
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)
	sec := skillsSectionOf(s)

	if sec.formFocus != skillFormName {
		t.Errorf("expected initial focus on Name (%d), got %d", skillFormName, sec.formFocus)
	}

	// tab moves forward
	next, _ = s.Update(tea.KeyPressMsg{Text: "tab", Code: tea.KeyTab})
	s = next.(Screen)
	sec = skillsSectionOf(s)
	if sec.formFocus != skillFormDescription {
		t.Errorf("expected focus on Description (%d), got %d", skillFormDescription, sec.formFocus)
	}

	// shift+tab moves backward
	next, _ = s.Update(tea.KeyPressMsg{Text: "shift+tab", Code: tea.KeyTab, Mod: tea.ModShift})
	s = next.(Screen)
	sec = skillsSectionOf(s)
	if sec.formFocus != skillFormName {
		t.Errorf("expected focus on Name (%d), got %d", skillFormName, sec.formFocus)
	}

	// shift+tab moves backward to Scope (0)
	next, _ = s.Update(tea.KeyPressMsg{Text: "shift+tab", Code: tea.KeyTab, Mod: tea.ModShift})
	s = next.(Screen)
	sec = skillsSectionOf(s)
	if sec.formFocus != skillFormScope {
		t.Errorf("expected focus on Scope (%d), got %d", skillFormScope, sec.formFocus)
	}

	// shift+tab wraps from Scope (0) to last field Instructions (6)
	next, _ = s.Update(tea.KeyPressMsg{Text: "shift+tab", Code: tea.KeyTab, Mod: tea.ModShift})
	s = next.(Screen)
	sec = skillsSectionOf(s)
	if sec.formFocus != skillFormInstructions {
		t.Errorf("expected focus on Instructions (%d), got %d", skillFormInstructions, sec.formFocus)
	}

	// down from Instructions wraps to Scope (0)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	sec = skillsSectionOf(s)
	if sec.formFocus != skillFormScope {
		t.Errorf("expected focus on Scope (%d), got %d", skillFormScope, sec.formFocus)
	}
	initialScope := sec.formFields[skillFormScope].Value()
	next, _ = s.Update(tea.KeyPressMsg{Text: " ", Code: tea.KeySpace})
	s = next.(Screen)
	sec = skillsSectionOf(s)
	newScope := sec.formFields[skillFormScope].Value()
	if initialScope == newScope {
		t.Errorf("expected Scope to cycle, stayed %q", initialScope)
	}
}

func TestSkillsSection_FormValidation(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusSkills(t, s)

	// Press 'n' to open Add form
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)

	// Press ctrl+s with empty Name
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("expected no save cmd when form validation fails")
	}
	s = next.(Screen)
	sec := skillsSectionOf(s)
	if !sec.editing {
		t.Error("expected to stay in editing mode on validation failure")
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Skill name is required") {
		t.Errorf("expected validation notice in view, got:\n%s", plain)
	}

	// Type invalid name with slashes
	for _, ch := range "foo/bar" {
		next, _ = s.Update(tea.KeyPressMsg{Text: string(ch), Code: ch})
		s = next.(Screen)
	}
	next, cmd = s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("expected no save cmd when name is invalid")
	}
	s = next.(Screen)
	plain = ansi.Strip(skillsSectionOf(s).View())
	if !strings.Contains(plain, "Invalid skill name") {
		t.Errorf("expected invalid skill name notice in view, got:\n%s", plain)
	}

	// Press esc to cancel
	next, _ = s.Update(tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc})
	s = next.(Screen)
	sec = skillsSectionOf(s)
	if sec.editing {
		t.Error("expected to exit editing mode after esc")
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

	// 'n' key opens editor
	next, _ := sec.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	sec = next.(*skillsSection)
	if !sec.editing {
		t.Error("expected 'n' key to open editor")
	}
	if hints := sec.Hints(); len(hints) != 4 {
		t.Errorf("expected 4 hints in editing mode, got %d", len(hints))
	}

	// 'esc' key exits editor
	next, _ = sec.Update(tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc})
	sec = next.(*skillsSection)
	if sec.editing {
		t.Error("expected 'esc' key to exit editor")
	}

	// 'e' key opens editor for selected skill
	next2, _ := sec.Update(tea.KeyPressMsg{Text: "e", Code: 'e'})
	sec = next2.(*skillsSection)
	if !sec.editing {
		t.Error("expected 'e' key to open editor for selected skill")
	}

	// SetSize
	sec.SetSize(120, 40)
	if sec.width != 120 || sec.height != 40 {
		t.Errorf("SetSize failed, got %dx%d", sec.width, sec.height)
	}
}
