package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
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
	if !strings.Contains(status, "THINKING") {
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
	if !strings.Contains(activeRow, "THINKING") || strings.HasPrefix(activeRow, " ") {
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

// TestStatusRowAdaptiveHintsWithSidebarOpen tests that when the sidebar is open
// on a constrained screen, fewer options are shown without clipping or breaking.
func TestStatusRowAdaptiveHintsWithSidebarOpen(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	s = next.(Screen)

	// Open sidebar (chatWidth becomes ~69)
	s.panel.open = true

	// Active turn with thinking status
	s.active = fakeHandle{id: "t1"}
	s.statusline.Start("thinking", fixedNow())

	row := ansi.Strip(s.statusRow())
	// Should not have the clip marker
	if strings.Contains(row, uikitconfig.ClipMarker) {
		t.Errorf("expected no clip marker in adaptive status row with sidebar open, got %q", row)
	}
	// Must contain high-priority cancel hint
	if !strings.Contains(row, "esc:cancel") {
		t.Errorf("expected esc:cancel in adaptive status row, got %q", row)
	}
	// Must fit inside chatWidth
	if ansi.StringWidth(row) > s.chatWidth() {
		t.Errorf("row width %d exceeds chatWidth %d: %q", ansi.StringWidth(row), s.chatWidth(), row)
	}
}

// TestStatusRowAdaptiveHintsPanelFocused tests navigation hint tiers when the panel is focused.
func TestStatusRowAdaptiveHintsPanelFocused(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	s = next.(Screen)

	s.panel.open = true
	s.panel.focused = true

	row := ansi.Strip(s.statusRow())
	if strings.Contains(row, uikitconfig.ClipMarker) {
		t.Errorf("expected no clip marker in panel focused status row, got %q", row)
	}
	if !strings.Contains(row, "tab:composer") {
		t.Errorf("expected tab:composer in panel focused status row, got %q", row)
	}
}

func TestHandleTurnEventUsageUpdatesTopbarAndStatusline(t *testing.T) {
	conv := &scriptedTestConversation{
		model: ports.ModelInfo{Name: "claude-3-7-sonnet", Provider: "anthropic", ContextWindow: 100_000},
		usage: ports.Usage{},
	}
	dark, _, themes := themePair(t)
	s := New(dark, theme.TierASCII, themes, conv, nil, 80, nil)
	s.statusline.Start("thinking", fixedNow())

	// Initial percentage should be 0%
	pct, ok := s.topbar.ContextPercent()
	if !ok || pct != 0 {
		t.Fatalf("expected initial pct 0, got %d (ok=%v)", pct, ok)
	}

	// Dispatch a mid-turn UsageBody event
	next, _ := s.handleTurnEvent(uievent.Event{
		Kind: uievent.KindUsage,
		Body: uievent.UsageBody{
			InputTokens:  42_000,
			OutputTokens: 3_000,
			CostUSD:      0.08,
		},
	})
	s = next.(Screen)

	// The gauge tracks the PROMPT the provider just priced: 42,000 of a
	// 100,000 budget. The 3,000 output tokens are not part of that prompt and
	// are counted once, as history, in the next request's own input.
	pct, ok = s.topbar.ContextPercent()
	if !ok || pct != 42 {
		t.Errorf("expected updated pct 42, got %d (ok=%v)", pct, ok)
	}

	// Statusline view should show cost but NO ctx pill
	status := s.statusline.View(fixedNow())
	if strings.Contains(status, "ctx") {
		t.Errorf("expected statusline to NOT contain ctx pill, got %q", status)
	}
	if !strings.Contains(status, "$0.08") {
		t.Errorf("expected statusline to contain $0.08, got %q", status)
	}
}
