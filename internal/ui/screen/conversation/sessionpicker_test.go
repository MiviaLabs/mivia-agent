package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func sampleSessions(now time.Time) []ports.SessionSummary {
	return []ports.SessionSummary{
		{
			ID:        "s-1",
			Title:     "Refactor Storage Engine",
			UpdatedAt: now.Add(-5 * time.Minute),
			Active:    true,
			State:     "running",
			Lines: []string{
				"> refactor storage engine",
				"Reading storage drivers in internal/storage...",
				"◈ running tool: list_files",
				"  found 12 storage implementation files",
				"Refactored memory caching layer.",
			},
		},
		{
			ID:        "s-2",
			Title:     "Fix Memory Leak in Transports",
			UpdatedAt: now.Add(-2 * time.Hour),
			Active:    false,
			State:     "done",
			Lines: []string{
				"> profile transport buffers",
				"Closed lingering idle TCP sockets.",
			},
		},
		{
			ID:        "s-3",
			Title:     "Add Comprehensive Integration Test Suite",
			UpdatedAt: now.Add(-24 * time.Hour),
			Active:    false,
			State:     "done",
		},
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		updated time.Time
		want    string
	}{
		{time.Time{}, ""},
		{now.Add(-10 * time.Second), "now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-2 * 24 * time.Hour), "2d ago"},
		{now.Add(-30 * 24 * time.Hour), "Jul 21"},
	}
	for _, tc := range cases {
		if got := formatRelativeTime(tc.updated, now); got != tc.want {
			t.Errorf("formatRelativeTime(%v) = %q, want %q", tc.updated, got, tc.want)
		}
	}
}

func TestSessionPickerFilterAndNavigation(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sp := newSessionPicker(th, theme.TierASCII, sampleSessions(now))

	if len(sp.visible()) != 3 {
		t.Fatalf("visible count = %d, want 3", len(sp.visible()))
	}
	sel, ok := sp.Selected()
	if !ok || sel.ID != "s-1" {
		t.Fatalf("initial selection = %+v, want s-1", sel)
	}

	// Down moves to next
	sp, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	sel, _ = sp.Selected()
	if sel.ID != "s-2" {
		t.Errorf("after Down selection = %q, want s-2", sel.ID)
	}

	// Filter by typing "leak"
	sp, _ = sp.Update(tea.KeyPressMsg{Text: "l"})
	sp, _ = sp.Update(tea.KeyPressMsg{Text: "e"})
	sp, _ = sp.Update(tea.KeyPressMsg{Text: "a"})
	sp, _ = sp.Update(tea.KeyPressMsg{Text: "k"})

	if len(sp.visible()) != 1 {
		t.Fatalf("filtered visible count = %d, want 1", len(sp.visible()))
	}
	sel, _ = sp.Selected()
	if sel.ID != "s-2" {
		t.Errorf("filtered selection = %q, want s-2", sel.ID)
	}

	// Backspace drops characters
	sp, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if sp.filter != "lea" {
		t.Errorf("after backspace filter = %q, want \"lea\"", sp.filter)
	}
}

func TestSessionPickerEnterAndEsc(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sp := newSessionPicker(th, theme.TierASCII, sampleSessions(now))

	// Enter emits SelectMsg
	_, cmd := sp.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected Cmd from Enter")
	}
	msg := cmd()
	selectMsg, ok := msg.(picker.SelectMsg)
	if !ok || selectMsg.Item != "s-1" {
		t.Errorf("Enter emitted %+v, want SelectMsg for s-1", msg)
	}

	// Esc emits CancelMsg
	_, cmd = sp.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected Cmd from Esc")
	}
	msg = cmd()
	if _, ok := msg.(picker.CancelMsg); !ok {
		t.Errorf("Esc emitted %+v, want CancelMsg", msg)
	}
}

func TestSessionPickerViewRendering(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sp := newSessionPicker(th, theme.TierASCII, sampleSessions(now))

	view := sp.View(th, theme.TierASCII, 60, now)
	plain := ansi.Strip(view)

	if !strings.Contains(plain, ">") {
		t.Errorf("missing cursor marker > in view:\n%s", plain)
	}
	if !strings.Contains(plain, "Refactor Storage Engine") {
		t.Errorf("missing session title in view:\n%s", plain)
	}
	if !strings.Contains(plain, "5m ago") {
		t.Errorf("missing relative timestamp in view:\n%s", plain)
	}

	// Dialog framing
	dialog := renderSessionPickerDialog(th, theme.TierASCII, 80, 24, sp, now)
	if !strings.Contains(dialog, "resume session") {
		t.Errorf("dialog missing title 'resume session':\n%s", dialog)
	}
	if !strings.Contains(dialog, "[enter] resume") {
		t.Errorf("dialog missing hint:\n%s", dialog)
	}
}

func TestSessionPickerPreviewToggleAndScroll(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sp := newSessionPicker(th, theme.TierASCII, sampleSessions(now))

	// Right arrow toggles preview ON
	sp, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !sp.preview {
		t.Fatal("expected preview mode to be true after Right arrow")
	}

	// Down arrow scrolls preview content
	sp, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if sp.previewOffset != 1 {
		t.Errorf("previewOffset = %d, want 1 after Down arrow", sp.previewOffset)
	}

	// Up arrow scrolls back
	sp, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if sp.previewOffset != 0 {
		t.Errorf("previewOffset = %d, want 0 after Up arrow", sp.previewOffset)
	}

	// Enter in preview mode still selects session
	_, cmd := sp.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected Cmd from Enter in preview mode")
	}
	msg := cmd()
	selectMsg, ok := msg.(picker.SelectMsg)
	if !ok || selectMsg.Item != "s-1" {
		t.Errorf("Enter emitted %+v, want SelectMsg for s-1", msg)
	}

	// Left arrow toggles preview OFF
	sp, _ = sp.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if sp.preview {
		t.Fatal("expected preview mode to be false after Left arrow")
	}
}

func TestSessionPickerPreviewRendering(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sp := newSessionPicker(th, theme.TierASCII, sampleSessions(now))
	sp.preview = true

	view := sp.PreviewView(th, theme.TierASCII, 60, 15, now)
	plain := ansi.Strip(view)

	if !strings.Contains(plain, "Refactor Storage Engine") {
		t.Errorf("preview missing title:\n%s", plain)
	}
	if !strings.Contains(plain, "Reading storage drivers") {
		t.Errorf("preview missing transcript lines:\n%s", plain)
	}

	// Dialog rendering in preview mode
	dialog := renderSessionPickerDialog(th, theme.TierASCII, 80, 24, sp, now)
	if !strings.Contains(dialog, "preview: Refactor Storage Engine") {
		t.Errorf("dialog in preview mode missing preview title:\n%s", dialog)
	}
	if !strings.Contains(dialog, "[←/→] list") {
		t.Errorf("dialog in preview mode missing list navigation hint:\n%s", dialog)
	}
}
