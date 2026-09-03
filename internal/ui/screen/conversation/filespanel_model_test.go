package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func modelPanelScreen(t *testing.T) Screen {
	t.Helper()
	s := panelScreen(t, uikitconfig.BreakpointWide, 24, sampleDiffs()...)
	s.SetCommandRunner(&fakeRunner{outcome: ports.CommandOutcome{ModelChoices: []string{"fast", "deep"}}})
	s.topbar.SetSession(ports.ModelInfo{Name: "claude-fable-5-1", Provider: "anthropic"}, ports.Usage{})
	return s
}

// TestOpenPanelLandsOnTheModelRow: opening the sidebar selects the model
// row first, and the row names the session's model.
func TestOpenPanelLandsOnTheModelRow(t *testing.T) {
	s := openPanel(t, modelPanelScreen(t))
	if got := s.panel.list.CursorRow(); got != 0 {
		t.Fatalf("cursor after open = %d, want 0 (the model row)", got)
	}
	view := ansi.Strip(s.View())
	// The screen refreshes the top bar's session from its conversation on
	// open, so assert against whatever model the bar now reports.
	name := s.topbar.Info().Name
	if name == "" || !strings.Contains(view, "> "+name) && !strings.Contains(view, "> "+s.topbar.Info().Provider+"/"+name) {
		t.Errorf("model row not marked as selected with the session's model %q:\n%s", name, view)
	}
	if strings.Contains(view, "SIDEBAR") {
		t.Errorf("the old SIDEBAR title must be gone:\n%s", view)
	}
}

// TestEnterOnModelRowOpensTheModelPicker: Enter on the model row opens
// the same picker "/model" does, not a content dialog.
func TestEnterOnModelRowOpensTheModelPicker(t *testing.T) {
	s := openPanel(t, modelPanelScreen(t))
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.modelPicker == nil {
		t.Fatal("Enter on the model row must open the model picker")
	}
	if s.panel.dialog {
		t.Error("the model row must not open a content dialog")
	}
}

// TestDoubleClickOnModelRowOpensTheModelPicker: one click selects the
// model row, a second within the double-click window opens the picker.
func TestDoubleClickOnModelRowOpensTheModelPicker(t *testing.T) {
	s := openPanel(t, modelPanelScreen(t))
	now := time.Now()
	s.now = func() time.Time { return now }
	next, _ := s.handleNavClick(2) // row 2 is the model row
	s = next.(Screen)
	if s.modelPicker != nil || s.panel.dialog {
		t.Fatal("a single click on the model row must only select it")
	}
	next, _ = s.handleNavClick(2)
	s = next.(Screen)
	if s.modelPicker == nil {
		t.Fatal("a double-click on the model row must open the model picker")
	}
}

// TestTopBarHidesTheModelCapsuleWhileTheSidebarIsOpen: the model is named
// once - in the sidebar while it is open, in the top bar otherwise.
func TestTopBarHidesTheModelCapsuleWhileTheSidebarIsOpen(t *testing.T) {
	s := modelPanelScreen(t)
	if _, _, ok := s.topbar.ModelBounds(); !ok {
		t.Fatal("precondition: the top bar shows the model capsule while the sidebar is closed")
	}
	s = openPanel(t, s)
	if _, _, ok := s.topbar.ModelBounds(); ok {
		t.Error("the top bar must hide its model capsule while the sidebar is open")
	}
	top := ansi.Strip(strings.Split(s.View(), "\n")[1])
	if strings.Contains(top, "claude-fable-5-1") {
		t.Errorf("top bar still names the model while the sidebar shows it:\n%s", top)
	}
}
