package conversation

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/keymap"
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

// TestEscHintPerState pins C9's truthfulness contract directly on
// escHint, independent of width fitting: a turn running says cancel, a
// queued message with nothing running says clear queue, and idle with
// nothing queued says nothing at all - the three states the status row
// must never blur together.
func TestEscHintPerState(t *testing.T) {
	t.Run("active turn: cancel", func(t *testing.T) {
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		s.active = fakeHandle{id: "t1"}
		part, ok := s.escHint()
		if !ok || part.Key != "esc" || part.Label != "cancel" {
			t.Errorf("got %+v ok=%v, want esc:cancel", part, ok)
		}
	})
	t.Run("idle with a queued message: clear queue", func(t *testing.T) {
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		s.queue = []string{"queued message"}
		part, ok := s.escHint()
		if !ok || part.Key != "esc" || part.Label != "clear queue" {
			t.Errorf("got %+v ok=%v, want esc:clear queue", part, ok)
		}
	})
	t.Run("idle with nothing queued: no hint", func(t *testing.T) {
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		if _, ok := s.escHint(); ok {
			t.Error("expected no esc hint while idle with an empty queue")
		}
	})
	t.Run("active turn wins over a queued message", func(t *testing.T) {
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		s.active = fakeHandle{id: "t1"}
		s.queue = []string{"queued message"}
		part, ok := s.escHint()
		if !ok || part.Label != "cancel" {
			t.Errorf("got %+v ok=%v, want cancel to take priority over a queued message", part, ok)
		}
	})
}

// TestStatusRowShowsClearQueueHintWhenIdleWithAQueuedMessage exercises
// the same three states end to end through statusRow, so the truthful
// label and the truthful width-fitting path are both proven, not just
// escHint in isolation.
func TestStatusRowShowsClearQueueHintWhenIdleWithAQueuedMessage(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	next, _ := s.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	s = next.(Screen)
	s.queue = []string{"queued message"}

	row := ansi.Strip(s.statusRow())
	if !strings.Contains(row, "esc:clear queue") {
		t.Errorf("got %q, want the clear-queue hint while idle with a queued message", row)
	}
	if strings.Contains(row, "esc:cancel") {
		t.Errorf("got %q, want no cancel hint while idle", row)
	}
}

// TestEscHintNeverShownIdleWithEmptyQueue is the negative half of the
// same contract, through statusRow: today's idle-with-nothing-queued
// row must still say nothing about esc at all.
func TestEscHintNeverShownIdleWithEmptyQueue(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	row := ansi.Strip(s.statusRow())
	if strings.Contains(row, "esc:") {
		t.Errorf("got %q, want no esc hint at all while idle with nothing queued", row)
	}
}

// TestEscClearsTheQueueWhenIdle is cancelTurn's own regression: the
// behaviour the new hint promises. Escape with a turn active still
// cancels the turn (unchanged); escape while idle with a queued
// message clears it; escape while idle with nothing queued is still
// the pre-existing no-op (returns handled=false, so a caller falls
// through to whatever else "esc" might mean, e.g. blurring focus).
func TestEscClearsTheQueueWhenIdle(t *testing.T) {
	t.Run("clears a non-empty queue", func(t *testing.T) {
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		s.queue = []string{"a", "b"}
		next, _, handled := s.cancelTurn()
		scr := next.(Screen)
		if !handled {
			t.Fatal("expected cancelTurn to handle esc with a queued message")
		}
		if len(scr.queue) != 0 {
			t.Errorf("got queue %v, want it cleared", scr.queue)
		}
		if len(scr.queueOverlay.Items()) != 0 {
			t.Errorf("got queueOverlay items %v, want them cleared too", scr.queueOverlay.Items())
		}
		// C9 review round 2: every OTHER queue mutation in this package
		// (handleQueueKey's delete, force-send's re-queue) tells the
		// user what happened via statusline.Notice - clearing the
		// WHOLE queue with one keypress is the biggest queue mutation
		// there is, and was the only silent one.
		if got := scr.statusline.View(fixedNow()); !strings.Contains(got, "queue cleared") {
			t.Errorf("got statusline %q, want a notice that the queue was cleared", got)
		}
	})
	t.Run("empty queue and no active turn stays a no-op", func(t *testing.T) {
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		_, _, handled := s.cancelTurn()
		if handled {
			t.Error("expected cancelTurn to report unhandled with nothing to cancel and nothing queued")
		}
	})
}

// TestEscHintNamesAKeyBoundInContextGlobal is the "no hint names an
// unbound key" gate, scoped to what C9 actually changed: the esc hint
// (escHint, both its cancel and its clear-queue form) must name a key
// keymap.Default genuinely binds to IDCancel in ContextGlobal, in every
// state that produces it - a hint promising a key does something the
// keymap does not back is exactly the lie ux-rules 1.4 forbids.
//
// This does not re-check the status row's OTHER, pre-existing hints
// (help/pager/panel/quit source from ContextGlobal, "?" for help
// sources from ContextComposer, and the panel-focused row's own
// navigation keys have no keymap.ID at all) - none of those are C9's
// scope, and asserting them here would pin pre-existing architecture
// this task was not asked to change.
func TestEscHintNamesAKeyBoundInContextGlobal(t *testing.T) {
	m := keymap.New(keymap.Default())
	assertEscBound := func(t *testing.T, part keymap.HintPart) {
		t.Helper()
		id, ok := m.Match(keymap.ContextGlobal, part.Key)
		if !ok {
			t.Fatalf("escHint named key %q, which ContextGlobal does not bind", part.Key)
		}
		if id != keymap.IDCancel {
			t.Errorf("escHint's key %q resolves to %q in ContextGlobal, want %q", part.Key, id, keymap.IDCancel)
		}
	}

	t.Run("active turn", func(t *testing.T) {
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		s.active = fakeHandle{id: "t1"}
		part, ok := s.escHint()
		if !ok {
			t.Fatal("expected an esc hint with a turn active")
		}
		assertEscBound(t, part)
	})
	t.Run("idle with a queued message", func(t *testing.T) {
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		s.queue = []string{"queued"}
		part, ok := s.escHint()
		if !ok {
			t.Fatal("expected an esc hint with a queued message")
		}
		assertEscBound(t, part)
	})
}
