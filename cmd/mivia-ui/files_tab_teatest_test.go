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

// newFilesRoot builds the real app root over the demo harness, the
// same construction newDemoHarnessRoot uses, for the Files-tab visual
// checks: a real tea.Program, a real event stream, real rendering.
func newFilesRoot(t *testing.T, scenario string) tea.Model {
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

// driveFilesTab runs the diff scenario (one approved edit), then
// ctrl+n into the Files tab, and returns the shadow stream's captured
// output once cond holds.
func driveFilesTab(t *testing.T, width int, cond func(string) bool) string {
	t.Helper()
	tm := teatest.NewTestModel(t, newFilesRoot(t, "approval-diff"), teatest.WithInitialTermSize(width, 30))
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
	tm.Send(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})

	// A fresh shadow from here on: the assertion reads only the Files
	// tab's frames, not the chat frames the composer's border was in.
	tab := &shadowStream{src: tm.Output()}
	for time.Now().Before(deadline) {
		tab.drain()
		if cond(tab.buf.String()) {
			return tab.buf.String()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met at width %d; captured:\n%s", width, tab.buf.String())
	return ""
}

// TestFilesTabVisualWide: at a wide terminal the tab renders as the
// split - two framed panes, file row on the left, the diff on the
// right, tab strip in the bar.
func TestFilesTabVisualWide(t *testing.T) {
	out := driveFilesTab(t, 140, func(s string) bool {
		return strings.Contains(s, "defaults.go") && strings.Count(s, "╭") >= 2
	})
	if !strings.Contains(out, "@@") {
		t.Errorf("wide files tab does not show the diff:\n%s", out)
	}
}

// TestFilesTabVisualNarrow: below the wide breakpoint the tab is the
// list alone - file rows present, no second pane.
func TestFilesTabVisualNarrow(t *testing.T) {
	out := driveFilesTab(t, 60, func(s string) bool {
		return strings.Contains(s, "defaults.go") && strings.Contains(s, "files")
	})
	if strings.Count(out, "╭") > 1 {
		t.Errorf("narrow files tab drew side-by-side panes:\n%s", out)
	}
}
