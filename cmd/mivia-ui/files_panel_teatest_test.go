package main

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	teatest "github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/MiviaLabs/mivia-agent/internal/ui/app"
	"github.com/MiviaLabs/mivia-agent/internal/ui/screen/conversation"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/demoharness"
)

// newPanelRoot builds the real app root over the demo harness, the same
// construction newDemoHarnessRoot uses, for the files-panel visual
// checks: a real tea.Program, a real event stream, real rendering.
func newPanelRoot(t *testing.T, scenario string) tea.Model {
	t.Helper()
	harness, err := demoharness.New(scenario, 0)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := theme.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var dark theme.Theme
	themes := make([]theme.Theme, 0, len(embedded))
	for _, c := range embedded {
		themes = append(themes, c)
		if c.Name == "mivia-dark" {
			dark = c
		}
	}
	screen := conversation.New(dark, theme.TierASCII, themes, harness, harness, 80, nil)
	screen.SetCommands(mockCommands())
	screen.SetCommandRunner(harness)
	return app.New(screen, dark, theme.TierASCII, themes)
}

// driveFilesPanel runs the diff scenario (one approved edit), then
// ctrl+n the panel open and (when asked) Enter the selected file's
// content dialog, and returns the shadow stream's captured output once
// cond holds.
func driveFilesPanel(t *testing.T, width int, openDialog bool, cond func(string) bool) string {
	t.Helper()
	tm := teatest.NewTestModel(t, newPanelRoot(t, "approval-diff"), teatest.WithInitialTermSize(width, 30))
	defer tm.Quit()
	shadow := &shadowStream{src: tm.Output()}

	// Type a prompt, run the turn, approve the pending edit; the demo
	// harness replays the script at pace 0.
	tm.Type("rename it")
	tm.Send(enterKey())
	tm.Send(enterKey()) // the completion menu takes the first Enter
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		shadow.drain()
		if strings.Contains(shadow.buf.String(), "o once") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tm.Send(tea.KeyPressMsg{Code: 'o'}) // approve: the edit's diff lands
	for time.Now().Before(deadline) {
		shadow.drain()
		if strings.Contains(shadow.buf.String(), "Renamed the constant") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tm.Send(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}) // panel: open, list focused
	if openDialog {
		tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	}

	// A fresh shadow from here on: the assertion reads only the panel's
	// frames, not the chat frames the composer's border was in.
	panel := &shadowStream{src: tm.Output()}
	for time.Now().Before(deadline) {
		panel.drain()
		if cond(panel.buf.String()) {
			return panel.buf.String()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met at width %d; captured:\n%s", width, panel.buf.String())
	return ""
}

// TestFilesPanelVisualWideDialog: at a wide terminal the panel splits
// the cockpit - the content dialog composes into the left column while
// the file list stays framed beside it.
func TestFilesPanelVisualWideDialog(t *testing.T) {
	out := driveFilesPanel(t, 140, true, func(s string) bool {
		// The dialog's body and the list pane's row, one frame: two
		// framed box: the dialog (the split itself draws only its rule).
		return strings.Contains(s, "defaults.go") && strings.Contains(s, "@@") && strings.Count(s, "╭") >= 1
	})
	if !strings.Contains(out, "any key closes") {
		t.Errorf("the content dialog's dismissal rule is not on screen:\n%s", out)
	}
}

// TestFilesPanelVisualNarrowList: below the wide breakpoint the panel
// collapses to the list over the transcript area - file rows present,
// no side-by-side panes, no dialog.
func TestFilesPanelVisualNarrowList(t *testing.T) {
	out := driveFilesPanel(t, 60, false, func(s string) bool {
		return strings.Contains(s, "defaults.go") && strings.Contains(s, "files changed")
	})
	if strings.Count(out, "╭") > 1 {
		t.Errorf("narrow panel drew side-by-side panes:\n%s", out)
	}
	if strings.Contains(out, "@@") {
		t.Errorf("narrow panel shows diff content without a dialog open:\n%s", out)
	}
}
