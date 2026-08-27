package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
)

func projectsSectionOf(s Screen) *projectsSection {
	for _, sec := range s.sections {
		if p, ok := sec.(*projectsSection); ok {
			return p
		}
	}
	return nil
}

func focusProjects(t *testing.T, s Screen) Screen {
	t.Helper()
	// Nav 0 is General, Nav 1 is Projects
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	if got := s.sections[s.nav].Title(); got != "Projects" {
		t.Fatalf("nav landed on %q, want Projects", got)
	}
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	return next.(Screen)
}

func awaitProjectsSaveTest(t *testing.T, s Screen, cmd tea.Cmd) Screen {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a Cmd from a Projects action")
	}
	next, _ := s.Update(cmd())
	return next.(Screen)
}

func TestProjectsSection_ListsAllFourteenFields(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)
	sec := projectsSectionOf(s)
	if len(sec.rows) != 14 {
		t.Fatalf("got %d rows, want 14", len(sec.rows))
	}

	plain := ansi.Strip(sec.View())
	expectedFields := []string{
		"Workspace Root:",
		"Config File:",
		"Env File:",
		"Branch Prefix:",
		"System Prompt:",
		"Temperature:",
		"Max Tokens:",
		"Max Prompt Tokens:",
		"Max Steps:",
		"Run Timeout:",
		"Storage Backend:",
		"Storage Path:",
		"Harness Sandbox:",
		"Redact Tool Args:",
	}
	for _, field := range expectedFields {
		if !strings.Contains(plain, field) {
			t.Errorf("Projects view missing expected field %q:\n%s", field, plain)
		}
	}
}

func TestProjectsSection_DetailPane(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 40)
	s = focusProjects(t, s)
	plain := ansi.Strip(projectsSectionOf(s).View())

	for _, want := range []string{"Workspace Root", "key:   [workspace.root]", "Filesystem root directory"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Projects detail pane missing %q:\n%s", want, plain)
		}
	}
}

func TestProjectsSection_Navigation(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)
	sec := projectsSectionOf(s)

	if sec.cursor != 0 {
		t.Errorf("initial cursor = %d, want 0", sec.cursor)
	}

	// down arrow moves to next row
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	sec = projectsSectionOf(s)
	if sec.cursor != 1 {
		t.Errorf("cursor after down = %d, want 1", sec.cursor)
	}

	// j moves down
	next, _ = s.Update(tea.KeyPressMsg{Text: "j", Code: 'j'})
	s = next.(Screen)
	sec = projectsSectionOf(s)
	if sec.cursor != 2 {
		t.Errorf("cursor after j = %d, want 2", sec.cursor)
	}

	// k moves up
	next, _ = s.Update(tea.KeyPressMsg{Text: "k", Code: 'k'})
	s = next.(Screen)
	sec = projectsSectionOf(s)
	if sec.cursor != 1 {
		t.Errorf("cursor after k = %d, want 1", sec.cursor)
	}

	// up arrow moves up
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	s = next.(Screen)
	sec = projectsSectionOf(s)
	if sec.cursor != 0 {
		t.Errorf("cursor after up = %d, want 0", sec.cursor)
	}

	// up at boundary clamps at 0
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	s = next.(Screen)
	sec = projectsSectionOf(s)
	if sec.cursor != 0 {
		t.Errorf("cursor clamped at top = %d, want 0", sec.cursor)
	}
}

func TestProjectsSection_ReadOnlyFields(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)

	// Row 0 is Workspace Root (read-only)
	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: tea.KeySpace})
	if cmd != nil {
		t.Error("expected no save cmd when activating read-only field")
	}
	s = next.(Screen)
	sec := projectsSectionOf(s)
	if sec.editing {
		t.Error("expected read-only field not to enter editing mode")
	}
	if !strings.Contains(sec.notice, "read-only") {
		t.Errorf("expected read-only notice, got %q", sec.notice)
	}

	// Row 1 is Config File (read-only)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	next, cmd = s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected no save cmd on read-only enter")
	}
	s = next.(Screen)
	sec = projectsSectionOf(s)
	if sec.editing {
		t.Error("expected read-only field not to enter editing mode")
	}
	if !strings.Contains(sec.notice, "read-only") {
		t.Errorf("expected read-only notice, got %q", sec.notice)
	}
}

func TestProjectsSection_ChoiceFieldsCycleAndPersist(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)

	// Move cursor down to Row 5 (Temperature)
	for i := 0; i < 5; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	sec := projectsSectionOf(s)
	if sec.cursor != 5 {
		t.Fatalf("cursor = %d, want 5 (Temperature)", sec.cursor)
	}

	origTemp := h.SettingsAdapters().Projects.Project().Temperature

	// Press space to cycle temperature
	next, cmd := s.Update(tea.KeyPressMsg{Text: " ", Code: tea.KeySpace})
	s = awaitProjectsSaveTest(t, next.(Screen), cmd)

	newTemp := h.SettingsAdapters().Projects.Project().Temperature
	if origTemp == newTemp {
		t.Errorf("expected Temperature to cycle from %q, got %q", origTemp, newTemp)
	}

	// Move to Storage Backend (Row 10)
	for i := 0; i < 5; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	sec = projectsSectionOf(s)
	if sec.cursor != 10 {
		t.Fatalf("cursor = %d, want 10 (Storage Backend)", sec.cursor)
	}

	origBackend := h.SettingsAdapters().Projects.Project().StoreBackend
	next, cmd = s.Update(tea.KeyPressMsg{Text: " ", Code: tea.KeySpace})
	s = awaitProjectsSaveTest(t, next.(Screen), cmd)

	newBackend := h.SettingsAdapters().Projects.Project().StoreBackend
	if origBackend == newBackend {
		t.Errorf("expected StoreBackend to cycle from %q, got %q", origBackend, newBackend)
	}
}

func TestProjectsSection_TextEditingAndValidation(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)

	// Move to Row 3 (Branch Prefix)
	for i := 0; i < 3; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}

	// Press Enter to start editing
	next, _ := s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	s = next.(Screen)
	sec := projectsSectionOf(s)
	if !sec.editing {
		t.Fatal("expected section to be in editing mode after enter")
	}

	// Set invalid branch prefix (does not end with /)
	sec.rows[sec.editRowIndex].f.SetValue("invalid-branch")

	// Press enter -> validation error
	next, cmd := s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	if cmd != nil {
		t.Error("expected no save cmd when branch prefix is invalid")
	}
	s = next.(Screen)
	sec = projectsSectionOf(s)
	if !sec.editing {
		t.Error("expected to stay in editing mode on validation error")
	}
	if !strings.Contains(sec.notice, "must end with /") {
		t.Errorf("expected validation notice for branch prefix, got %q", sec.notice)
	}

	// Set valid branch prefix
	sec.rows[sec.editRowIndex].f.SetValue("feature/")

	// Press enter -> saves and persists
	next, cmd = s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	s = awaitProjectsSaveTest(t, next.(Screen), cmd)

	sec = projectsSectionOf(s)
	if sec.editing {
		t.Error("expected to exit editing mode after successful save")
	}
	if got := h.SettingsAdapters().Projects.Project().BranchPrefix; got != "feature/" {
		t.Errorf("expected BranchPrefix in store to be 'feature/', got %q", got)
	}
}

func TestProjectsSection_CancelEditingWithEsc(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)

	// Move to Row 2 (Env File)
	for i := 0; i < 2; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}

	origEnv := h.SettingsAdapters().Projects.Project().EnvFile

	// Press Enter to edit
	next, _ := s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	s = next.(Screen)
	sec := projectsSectionOf(s)
	if !sec.editing {
		t.Fatal("expected section in editing mode")
	}

	// Change value in editor
	sec.rows[sec.editRowIndex].f.SetValue("./.env.custom")

	// Press esc to cancel
	next, _ = s.Update(tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc})
	s = next.(Screen)
	sec = projectsSectionOf(s)
	if sec.editing {
		t.Error("expected editing to be false after esc")
	}

	if got := h.SettingsAdapters().Projects.Project().EnvFile; got != origEnv {
		t.Errorf("expected EnvFile to remain %q, got %q", origEnv, got)
	}
}

func TestProjectsSection_NoCreationNotice(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)

	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)
	sec := projectsSectionOf(s)
	if !strings.Contains(sec.notice, "creation is not available") {
		t.Errorf("expected no-creation notice, got %q", sec.notice)
	}
}

func TestProjectsSection_NoDeletionNotice(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)

	next, _ := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = next.(Screen)
	sec := projectsSectionOf(s)
	if !strings.Contains(sec.notice, "deletion is not available") {
		t.Errorf("expected no-deletion notice, got %q", sec.notice)
	}
}

func TestProjectsSection_CapturingInput(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)
	sec := projectsSectionOf(s)

	if sec.CapturingInput() {
		t.Error("expected CapturingInput to be false when not editing")
	}

	// Move to Row 4 (System Prompt) and enter edit mode
	for i := 0; i < 4; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	next, _ := s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	s = next.(Screen)
	sec = projectsSectionOf(s)

	if !sec.CapturingInput() {
		t.Error("expected CapturingInput to be true while editing text")
	}
}

func TestProjectsSection_UnavailableWhenStoreNil(t *testing.T) {
	sec := newProjectsSection(nil)
	if sec.Title() != "Projects" {
		t.Errorf("Title = %q, want Projects", sec.Title())
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "unavailable") {
		t.Errorf("expected unavailable message, got %q", plain)
	}

	next, cmd := sec.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	if cmd != nil {
		t.Error("expected nil cmd from nil store")
	}
	if next.(*projectsSection).editing {
		t.Error("expected editing false with nil store")
	}
}

func TestProjectsSection_Hints(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusProjects(t, s)
	sec := projectsSectionOf(s)

	hints := sec.Hints()
	if len(hints) == 0 {
		t.Error("expected non-empty Hints()")
	}

	// Enter edit mode on Row 2
	for i := 0; i < 2; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	next, _ := s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	s = next.(Screen)
	sec = projectsSectionOf(s)

	editHints := sec.Hints()
	if len(editHints) != 2 || editHints[0] != keymap.IDSettingsToggle || editHints[1] != keymap.IDSettingsBack {
		t.Errorf("unexpected edit hints: %v", editHints)
	}
}
