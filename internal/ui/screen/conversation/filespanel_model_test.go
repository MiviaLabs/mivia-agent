package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/render"
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
	next, _ := s.handleNavClick(4) // row 4 is the model row (after the context section)
	s = next.(Screen)
	if s.modelPicker != nil || s.panel.dialog {
		t.Fatal("a single click on the model row must only select it")
	}
	next, _ = s.handleNavClick(4)
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

// TestSidebarContextSectionOwnsTheShareWhileOpen: the context share
// moves out of the top bar and off the model row into its own section,
// a header with the share right-aligned and a bar the full inner width.
func TestSidebarContextSectionOwnsTheShareWhileOpen(t *testing.T) {
	half := func(s Screen) Screen {
		s.topbar.SetSession(
			ports.ModelInfo{Name: "claude-fable-5-1", Provider: "anthropic", ContextWindow: 100_000},
			ports.Usage{InputTokens: 50_000},
		)
		return s
	}
	s := half(modelPanelScreen(t))
	if !strings.Contains(ansi.Strip(strings.Split(s.View(), "\n")[1]), "50%") {
		t.Fatal("precondition: the top bar shows the context share while the sidebar is closed")
	}
	s = half(openPanel(t, s)) // opening replays the fixture session; re-seed the share
	lines := strings.Split(ansi.Strip(s.View()), "\n")
	if strings.Contains(lines[1], "50%") {
		t.Errorf("top bar still shows the context share while the sidebar owns it:\n%s", lines[1])
	}
	var header, bar, model string
	for i, l := range lines {
		if strings.Contains(l, "context") && strings.Contains(l, "50%") && i+3 < len(lines) {
			header, bar, model = l, lines[i+1], lines[i+3]
			break
		}
	}
	if header == "" {
		t.Fatalf("no 'context ... 50%%' header in the sidebar:\n%s", strings.Join(lines, "\n"))
	}
	want := render.ContextBar(50, s.panelInnerWidth(), s.Tier)
	if !strings.Contains(bar, want) {
		t.Errorf("bar row %q lacks the half-filled full-width bar %q", bar, want)
	}
	if strings.Contains(model, "%") {
		t.Errorf("model row still carries the context share: %q", model)
	}
}
