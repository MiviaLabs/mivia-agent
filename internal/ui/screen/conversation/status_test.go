package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

// TestStatusRowShowsStatusTimerAndRightAlignedCancelHintDuringTurn tests
// that the active-turn status line shows status and timer on the left,
// without duplicate ctx percentage, and right-aligned key hints including
// cancel affordance.
func TestStatusRowShowsStatusTimerAndRightAlignedCancelHintDuringTurn(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())
	s.topbar.SetSession(ports.ModelInfo{Name: "m", ContextWindow: 100_000},
		ports.Usage{InputTokens: 40_000, OutputTokens: 22_000})

	status := s.statusText()
	if !strings.Contains(status, "thinking") {
		t.Errorf("got %q, want thinking status", status)
	}
	if strings.Contains(status, "ctx") {
		t.Errorf("got %q, want no ctx share in bottom status text", status)
	}

	row := ansi.Strip(s.statusRow())
	if !strings.Contains(row, "esc:cancel") {
		t.Errorf("got %q, want cancel hint in status row", row)
	}
}

// TestStatusTextHasNoCancelHintWithoutAnActiveTurn: with no
// turn running there is nothing to cancel.
func TestStatusTextHasNoCancelHintWithoutAnActiveTurn(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	row := ansi.Strip(s.statusRow())
	if strings.Contains(row, "esc:cancel") {
		t.Errorf("got %q, want no cancel hint with no active turn", row)
	}
}

// TestStatusRowRightAlignmentLayout tests that hints are placed on the right side
// and status/timer is placed on the left side of the bottom status row.
func TestStatusRowRightAlignmentLayout(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	s = next.(Screen)

	// Idle state: hints should be right-aligned (line has leading padding spaces)
	idleRow := ansi.Strip(s.statusRow())
	if !strings.HasPrefix(idleRow, " ") {
		t.Errorf("expected right-aligned idle status row to have leading spaces, got %q", idleRow)
	}
	if !strings.HasSuffix(idleRow, "ctrl+c:quit") {
		t.Errorf("expected idle status row to end with key hint, got %q", idleRow)
	}

	// Active state: left has status and timer, right has hints
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())
	activeRow := ansi.Strip(s.statusRow())
	if !strings.Contains(activeRow, "thinking") || strings.HasPrefix(activeRow, " ") {
		t.Errorf("expected active status row to start with status on the left, got %q", activeRow)
	}
	if !strings.HasSuffix(activeRow, "ctrl+c:quit") {
		t.Errorf("expected active status row to end with key hint on the right, got %q", activeRow)
	}
}

// TestStatusRowClipsWithTheSharedClipMarker pins wireframes-panes.md
// section 8/14's shared clip glyph for the status row's own final
// width clamp - a separate truncation from the screen-edge gutter's
// own, since it runs on the composed line before gutter ever sees it.
func TestStatusRowClipsWithTheSharedClipMarker(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 20, Height: 24})
	s = next.(Screen)
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())
	s.topbar.SetSession(ports.ModelInfo{Name: "m", ContextWindow: 100_000},
		ports.Usage{InputTokens: 40_000, OutputTokens: 22_000})

	got := ansi.Strip(s.statusRow())
	if !strings.Contains(got, uikitconfig.ClipMarker) {
		t.Errorf("got %q, want the clip marker %q on the clipped status row", got, uikitconfig.ClipMarker)
	}
}
