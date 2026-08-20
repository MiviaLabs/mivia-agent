package settings

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/topbar"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// agentsSectionOf reaches the Agents section (nav index 3).
func agentsSectionOf(s Screen) *agentsSection { return s.sections[3].(*agentsSection) }

func focusAgents(t *testing.T, s Screen) Screen {
	t.Helper()
	for i := 0; i < 3; i++ {
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

func TestAgentsSectionListsEveryAgentWithPromptLengthNotText(t *testing.T) {
	s, _ := newHarnessScreen(t, 100, 30)
	plain := ansi.Strip(agentsSectionOf(s).View())
	for _, want := range []string{ports.DefaultAgentName, "go-engineer", "prompt 4200 chars", "prompt 2100 chars"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Agents view is missing %q:\n%s", want, plain)
		}
	}
}

func TestRemovingAnAgentUpdatesTheStore(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)

	// Row 0 is the default agent (undeletable); move to row 1.
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	s = next.(Screen)
	target := agentsSectionOf(s).rows[agentsSectionOf(s).cursor].Name
	if target == ports.DefaultAgentName {
		t.Fatal("test setup landed on the default agent; fixture order changed")
	}

	next, cmd := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = awaitAgentsSaveTest(t, next.(Screen), cmd)

	for _, a := range h.SettingsAdapters().Agents.Agents() {
		if a.Name == target {
			t.Errorf("agent %q still present after removal", target)
		}
	}
}

// TestRemovingTheDefaultAgentFailsAndKeepsIt exercises the fake's own
// guard (demoharness/settings_agents.go) end to end through the
// section: the default agent cannot be removed, and the failure must
// surface as a notice, not silently vanish it from the list.
func TestRemovingTheDefaultAgentFailsAndKeepsIt(t *testing.T) {
	s, h := newHarnessScreen(t, 100, 30)
	s = focusAgents(t, s)
	if got := agentsSectionOf(s).rows[0].Name; got != ports.DefaultAgentName {
		t.Fatalf("row 0 is %q, want the default agent %q", got, ports.DefaultAgentName)
	}

	next, cmd := s.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	s = awaitAgentsSaveTest(t, next.(Screen), cmd)

	if agentsSectionOf(s).notice == "" {
		t.Error("expected a notice explaining the default agent cannot be removed")
	}
	found := false
	for _, a := range h.SettingsAdapters().Agents.Agents() {
		if a.Name == ports.DefaultAgentName {
			found = true
		}
	}
	if !found {
		t.Error("the default agent was removed despite the fake's guard")
	}
}

func TestUnavailableAgentsSectionSaysSo(t *testing.T) {
	th := loadTheme(t)
	tb := topbar.New(th, theme.TierTrueColor, ports.ModelInfo{}, ports.Usage{}, 80)
	s := New(th, theme.TierTrueColor, tb, ports.Settings{}, 0)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	s = next.(Screen)
	if got := ansi.Strip(agentsSectionOf(s).View()); !strings.Contains(got, "unavailable") {
		t.Errorf("expected the nil-store Agents section to say unavailable, got %q", got)
	}
}
