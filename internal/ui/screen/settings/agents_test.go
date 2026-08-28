package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// agentsSectionOf reaches the Agents section (nav index 2).
func agentsSectionOf(s Screen) *agentsSection { return s.sections[2].(*agentsSection) }

func focusAgents(t *testing.T, s Screen) Screen {
	t.Helper()
	for i := 0; i < 2; i++ {
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
	}
	if got := s.sections[s.nav].Title(); got != "Agents" {
		t.Fatalf("nav landed on %q, want Agents", got)
	}
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	return next.(Screen)
}

func awaitAgentsSaveTest(t *testing.T, s Screen, cmd tea.Cmd) Screen {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a Cmd from an Agents action")
	}
	next, _ := s.Update(cmd())
	return next.(Screen)
}

func TestAgentsSection_GroupedByGlobalAndProject(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)
	plain := ansi.Strip(agentsSectionOf(s).View())

	for _, header := range []string{"Global Agents (user home)", "Project Agents (workspace)", "Built-in Agents (shipped with mivia)"} {
		if !strings.Contains(plain, header) {
			t.Errorf("Agents view is missing group header %q:\n%s", header, plain)
		}
	}
	for _, agent := range []string{"general-purpose", "go-engineer", "reviewer"} {
		if !strings.Contains(plain, agent) {
			t.Errorf("Agents view is missing agent %q:\n%s", agent, plain)
		}
	}
}

func TestAgentsSection_DetailPane(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)
	plain := ansi.Strip(agentsSectionOf(s).View())

	for _, want := range []string{"Global (user home:", "model: openrouter/anthropic/claude-opus-5", "System Prompt:"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Agents detail pane missing %q:\n%s", want, plain)
		}
	}
}

func TestAgentsSection_RemoveAgent_RequiresConfirmation(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// go-engineer is the first selectable row (the built-in group renders last)
	sec := agentsSectionOf(s)
	target, ok := sec.selectedAgent()
	if !ok || target.Name != "go-engineer" {
		t.Fatalf("expected go-engineer selected, got %v", target)
	}

	// 1. First 'x' triggers confirmation notice
	next, cmd := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if cmd != nil {
		t.Fatal("expected no immediate save cmd on first 'x'")
	}
	s = next.(Screen)
	plain := ansi.Strip(agentsSectionOf(s).View())
	if !strings.Contains(plain, "Delete agent \"go-engineer\"? Press 'x' or 'y' to confirm") {
		t.Fatalf("expected confirmation prompt on first 'x', got:\n%s", plain)
	}

	// 2. Press 'esc' to cancel deletion
	next, _ = s.Update(tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc})
	s = next.(Screen)
	if strings.Contains(ansi.Strip(agentsSectionOf(s).View()), "Delete agent") {
		t.Error("expected confirmation prompt to be cleared after esc")
	}

	// Verify agent still exists in store
	found := false
	for _, a := range h.SettingsAdapters().Agents.Agents() {
		if a.Name == target.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("agent was removed prematurely after esc")
	}

	// 3. Press 'x' again and confirm with 'x'
	next, _ = s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = next.(Screen)
	next, cmd = s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = awaitAgentsSaveTest(t, next.(Screen), cmd)

	for _, a := range h.SettingsAdapters().Agents.Agents() {
		if a.Name == target.Name {
			t.Errorf("agent %q still present after confirmed removal", target.Name)
		}
	}
}

func TestAgentsSection_DeleteConfirmation_AnyKeyCancelsWithoutSideEffect(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Move to row 1
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	sec := agentsSectionOf(s)
	initialCursor := sec.cursor

	// Press 'x' to trigger confirmation
	next, _ = s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = next.(Screen)
	sec = agentsSectionOf(s)
	if sec.confirmDeleteName == "" {
		t.Fatal("expected confirmDeleteName to be set")
	}

	// Press 'j' (down navigation) - should cancel confirmation without moving cursor
	next, _ = s.Update(tea.KeyPressMsg{Text: "j", Code: 'j'})
	s = next.(Screen)
	sec = agentsSectionOf(s)
	if sec.confirmDeleteName != "" {
		t.Errorf("expected confirmDeleteName to be cleared")
	}
	if sec.cursor != initialCursor {
		t.Errorf("cursor changed on cancelled delete, got %d want %d", sec.cursor, initialCursor)
	}
}

// TestAgentsSection_RemoveDefaultAgentFails keeps its historical name (the
// deletions allowlist gate rejects renames in the same commit); the body now
// pins the scope-based guard: a built-in row cannot be removed.
func TestAgentsSection_RemoveDefaultAgentFails(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Navigate down to the built-in row (the last selectable row).
	sec := agentsSectionOf(s)
	for i := 0; i < 10; i++ {
		if ag, ok := sec.selectedAgent(); ok && ag.Name == "general-purpose" && ag.Scope == ports.ScopeBuiltin {
			break
		}
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
		sec = agentsSectionOf(s)
	}
	if ag, ok := sec.selectedAgent(); !ok || ag.Name != "general-purpose" || ag.Scope != ports.ScopeBuiltin {
		t.Fatalf("failed to reach the built-in general-purpose row, cursor on %v", ag)
	}

	// Press 'x' on the built-in agent
	next, cmd := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if cmd != nil {
		t.Fatal("expected no save cmd when deleting a built-in agent")
	}
	s = next.(Screen)
	sec = agentsSectionOf(s)
	if !strings.Contains(sec.notice, "built-in agent \"general-purpose\" cannot be removed") {
		t.Errorf("expected notice about the built-in agent, got %q", sec.notice)
	}

	found := false
	for _, a := range h.SettingsAdapters().Agents.Agents() {
		if a.Name == "general-purpose" {
			found = true
		}
	}
	if !found {
		t.Error("the built-in agent was removed")
	}
}

func fillAgentForm(s Screen) Screen {
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	s = typeText(s, "test-planner")
	next, _ := s.Update(down) // Description
	s = typeText(next.(Screen), "plans complex test scenarios")
	next, _ = s.Update(down) // Provider
	s = typeText(next.(Screen), "deepseek")
	next, _ = s.Update(down) // Model
	s = typeText(next.(Screen), "deepseek-v4-flash")
	next, _ = s.Update(down) // Tools
	s = typeText(next.(Screen), "read_file, run_command")
	next, _ = s.Update(down) // Skills
	s = typeText(next.(Screen), "test-runner")
	next, _ = s.Update(down) // MCP
	s = typeText(next.(Screen), "github")
	next, _ = s.Update(down) // System Prompt
	return typeText(next.(Screen), "You are a test scenario planner.")
}

func TestAddingAnAgent_FormAndPersist(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Press 'n' to open Add form
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)
	sec := agentsSectionOf(s)
	if !sec.editing {
		t.Fatal("expected section to be in editing mode after 'n'")
	}
	if plain := ansi.Strip(sec.View()); !strings.Contains(plain, "Add New Agent") {
		t.Fatalf("expected Add New Agent header in view, got:\n%s", plain)
	}

	s = fillAgentForm(s)

	// Press ctrl+s to save
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	s = awaitAgentsSaveTest(t, next.(Screen), cmd)

	var created *ports.AgentView
	for _, ag := range h.SettingsAdapters().Agents.Agents() {
		if ag.Name == "test-planner" {
			agCopy := ag
			created = &agCopy
			break
		}
	}
	if created == nil {
		t.Fatal("expected new agent 'test-planner' in store, but not found")
	}
	if created.Description != "plans complex test scenarios" {
		t.Errorf("got description %q, want 'plans complex test scenarios'", created.Description)
	}
	if created.Provider != "deepseek" || created.Model != "deepseek-v4-flash" {
		t.Errorf("got provider/model %s/%s, want deepseek/deepseek-v4-flash", created.Provider, created.Model)
	}
	if len(created.Tools) != 2 || created.Tools[0] != "read_file" || created.Tools[1] != "run_command" {
		t.Errorf("got tools %v, want ['read_file', 'run_command']", created.Tools)
	}
	if len(created.Skills) != 1 || created.Skills[0] != "test-runner" {
		t.Errorf("got skills %v, want ['test-runner']", created.Skills)
	}
}

func TestEditingAnAgent_FormAndPersist(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Move cursor to reviewer (one down from go-engineer)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	// Press 'enter' on selected agent (reviewer)
	next, _ = s.Update(tea.KeyPressMsg{Text: "enter", Code: tea.KeyEnter})
	s = next.(Screen)
	sec := agentsSectionOf(s)
	if !sec.editing {
		t.Fatal("expected section to be in editing mode after 'enter'")
	}
	if plain := ansi.Strip(sec.View()); !strings.Contains(plain, "Edit Agent: reviewer") {
		t.Fatalf("expected Edit Agent header in view, got:\n%s", plain)
	}

	// Update Description
	sec.formFields[agentFormDescription].SetValue("super reviewer for pull requests")

	// Press ctrl+s to save
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	s = awaitAgentsSaveTest(t, next.(Screen), cmd)

	var updated *ports.AgentView
	for _, ag := range h.SettingsAdapters().Agents.Agents() {
		if ag.Name == "reviewer" {
			agCopy := ag
			updated = &agCopy
			break
		}
	}
	if updated == nil {
		t.Fatal("agent 'reviewer' missing from store after edit")
	}
	if updated.Description != "super reviewer for pull requests" {
		t.Errorf("got description %q, want 'super reviewer for pull requests'", updated.Description)
	}
}

func TestRenamingAnAgent_FormAndPersist(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Move cursor to reviewer (Project scope, one down from go-engineer)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)

	// Press 'enter' to edit
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	// Move up to Name field
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	s = next.(Screen)

	// Change name
	sec := agentsSectionOf(s)
	sec.formFields[agentFormName].SetValue("reviewer-v2")

	// Press ctrl+s to save
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	s = awaitAgentsSaveTest(t, next.(Screen), cmd)

	foundOld := false
	foundNew := false
	for _, ag := range h.SettingsAdapters().Agents.Agents() {
		if ag.Name == "reviewer" {
			foundOld = true
		}
		if ag.Name == "reviewer-v2" {
			foundNew = true
		}
	}
	if foundOld {
		t.Error("expected old agent 'reviewer' to be removed on rename")
	}
	if !foundNew {
		t.Error("expected new agent 'reviewer-v2' to exist after rename")
	}
}

// TestAgentsSection_DefaultAgentNameCannotBeRenamed keeps its historical
// name (the deletions allowlist gate rejects renames in the same commit); the
// body now pins that a built-in row cannot be edited at all.
func TestAgentsSection_DefaultAgentNameCannotBeRenamed(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Navigate down to the built-in row (the last selectable row).
	sec := agentsSectionOf(s)
	for i := 0; i < 10; i++ {
		if ag, ok := sec.selectedAgent(); ok && ag.Name == "general-purpose" && ag.Scope == ports.ScopeBuiltin {
			break
		}
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		s = next.(Screen)
		sec = agentsSectionOf(s)
	}

	// Press 'enter' on the built-in row: the editor must not open.
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	sec = agentsSectionOf(s)
	if sec.editing {
		t.Fatal("the editor must not open for a built-in agent")
	}
	if !strings.Contains(sec.notice, "built-in agent \"general-purpose\" is read-only") {
		t.Errorf("expected read-only notice, got %q", sec.notice)
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "built-in agent \"general-purpose\" is read-only") {
		t.Errorf("expected read-only notice in view, got:\n%s", plain)
	}
}

func TestAgentsSection_DuplicateNameValidation(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Press 'n' to add agent
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)

	// Set duplicate project agent name: "reviewer"
	sec := agentsSectionOf(s)
	sec.formFields[agentFormName].SetValue("reviewer")

	// Press ctrl+s
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("expected no save cmd when duplicate name is entered")
	}
	s = next.(Screen)
	plain := ansi.Strip(agentsSectionOf(s).View())
	if !strings.Contains(plain, "already exists") {
		t.Errorf("expected duplicate name warning in view, got:\n%s", plain)
	}
}

func TestAgentsSection_FormNavigation(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Press 'n' to open Add form
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)
	sec := agentsSectionOf(s)

	if sec.formFocus != agentFormName {
		t.Errorf("expected initial focus on Name (%d), got %d", agentFormName, sec.formFocus)
	}

	// tab moves forward to Description (2)
	next, _ = s.Update(tea.KeyPressMsg{Text: "tab", Code: tea.KeyTab})
	s = next.(Screen)
	sec = agentsSectionOf(s)
	if sec.formFocus != agentFormDescription {
		t.Errorf("expected focus on Description (%d), got %d", agentFormDescription, sec.formFocus)
	}

	// shift+tab moves backward to Name (1)
	next, _ = s.Update(tea.KeyPressMsg{Text: "shift+tab", Code: tea.KeyTab, Mod: tea.ModShift})
	s = next.(Screen)
	sec = agentsSectionOf(s)
	if sec.formFocus != agentFormName {
		t.Errorf("expected focus on Name (%d), got %d", agentFormName, sec.formFocus)
	}

	// shift+tab moves backward to Scope (0)
	next, _ = s.Update(tea.KeyPressMsg{Text: "shift+tab", Code: tea.KeyTab, Mod: tea.ModShift})
	s = next.(Screen)
	sec = agentsSectionOf(s)
	if sec.formFocus != agentFormScope {
		t.Errorf("expected focus on Scope (%d), got %d", agentFormScope, sec.formFocus)
	}

	// shift+tab wraps from Scope (0) to System Prompt (8)
	next, _ = s.Update(tea.KeyPressMsg{Text: "shift+tab", Code: tea.KeyTab, Mod: tea.ModShift})
	s = next.(Screen)
	sec = agentsSectionOf(s)
	if sec.formFocus != agentFormSystemPrompt {
		t.Errorf("expected focus on System Prompt (%d), got %d", agentFormSystemPrompt, sec.formFocus)
	}

	// down from System Prompt wraps to Scope (0)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	sec = agentsSectionOf(s)
	if sec.formFocus != agentFormScope {
		t.Errorf("expected focus on Scope (%d), got %d", agentFormScope, sec.formFocus)
	}
	initialScope := sec.formFields[agentFormScope].Value()
	next, _ = s.Update(tea.KeyPressMsg{Text: " ", Code: tea.KeySpace})
	s = next.(Screen)
	sec = agentsSectionOf(s)
	newScope := sec.formFields[agentFormScope].Value()
	if initialScope == newScope {
		t.Errorf("expected Scope to cycle, stayed %q", initialScope)
	}
}

func TestAgentsSection_FormValidation(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Press 'n' to open Add form
	next, _ := s.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	s = next.(Screen)

	// Press ctrl+s with empty Name
	next, cmd := s.Update(tea.KeyPressMsg{Text: "ctrl+s", Code: 's', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("expected no save cmd when form validation fails")
	}
	s = next.(Screen)
	sec := agentsSectionOf(s)
	if !sec.editing {
		t.Error("expected to stay in editing mode on validation failure")
	}
	plain := ansi.Strip(sec.View())
	if !strings.Contains(plain, "Agent name is required") {
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
	plain = ansi.Strip(agentsSectionOf(s).View())
	if !strings.Contains(plain, "Invalid agent name") {
		t.Errorf("expected invalid agent name notice in view, got:\n%s", plain)
	}

	// Press esc to cancel
	next, _ = s.Update(tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc})
	s = next.(Screen)
	sec = agentsSectionOf(s)
	if sec.editing {
		t.Error("expected to exit editing mode after esc")
	}
}

func TestAgentsSection_NilStore(t *testing.T) {
	sec := newAgentsSection(nil)
	if sec.Title() != "Agents" {
		t.Errorf("Title() = %q, want Agents", sec.Title())
	}
	if !strings.Contains(sec.View(), "unavailable") {
		t.Errorf("View() = %q, want unavailable message", sec.View())
	}
	next, cmd := sec.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	if cmd != nil {
		t.Error("expected nil cmd from nil store update")
	}
	if next.(*agentsSection).editing {
		t.Error("expected not to enter editing mode with nil store")
	}
}

func TestAgentsSection_HintsAndNotices(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)
	sec := agentsSectionOf(s)

	if hints := sec.Hints(); len(hints) == 0 {
		t.Error("expected non-empty Hints()")
	}

	// 'n' key opens editor
	next, _ := sec.Update(tea.KeyPressMsg{Text: "n", Code: 'n'})
	sec = next.(*agentsSection)
	if !sec.editing {
		t.Error("expected 'n' key to open editor")
	}
	if hints := sec.Hints(); len(hints) != 4 {
		t.Errorf("expected 4 hints in editing mode, got %d", len(hints))
	}

	// 'esc' key exits editor
	next, _ = sec.Update(tea.KeyPressMsg{Text: "esc", Code: tea.KeyEsc})
	sec = next.(*agentsSection)
	if sec.editing {
		t.Error("expected 'esc' key to exit editor")
	}

	// 'e' key opens editor for selected agent
	next2, _ := sec.Update(tea.KeyPressMsg{Text: "e", Code: 'e'})
	sec = next2.(*agentsSection)
	if !sec.editing {
		t.Error("expected 'e' key to open editor for selected agent")
	}
}
