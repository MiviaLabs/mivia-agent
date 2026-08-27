package conversation

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/MiviaLabs/mivia-agent/internal/ui/component/picker"
	"github.com/MiviaLabs/mivia-agent/internal/ui/theme"
	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
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

// TestSessionPickerClipsALongTitleWithTheSharedClipMarker pins
// wireframes-panes.md section 8/14's shared clip glyph: every clipped
// row in the UI ends with uikitconfig.ClipMarker ("~"), not an ad hoc
// ellipsis, so a cut row reads the same way everywhere.
func TestSessionPickerClipsALongTitleWithTheSharedClipMarker(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sessions := []ports.SessionSummary{{
		ID:        "s-long",
		Title:     strings.Repeat("a very long session title ", 6),
		UpdatedAt: now.Add(-5 * time.Minute),
		State:     "idle",
	}}
	sp := newSessionPicker(th, theme.TierASCII, sessions)

	view := sp.View(th, theme.TierASCII, 40, now)
	plain := ansi.Strip(view)
	if !strings.Contains(plain, uikitconfig.ClipMarker) {
		t.Errorf("got %q, want the shared clip marker %q on the truncated title", plain, uikitconfig.ClipMarker)
	}
	if strings.Contains(plain, "…") {
		t.Errorf("got %q, want no ad hoc ellipsis left over", plain)
	}
}

// TestSessionPickerShowsTurnCountAndContextSize pins
// wireframes-panes.md section 12.2's row shape: "s3-retry-backoff
// 14 turns   2h ago    41k ctx". Before this test, ports.SessionSummary
// carried no turn count or context size at all, so neither could ever
// appear in the row regardless of what an adapter might report.
func TestSessionPickerShowsTurnCountAndContextSize(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sessions := []ports.SessionSummary{{
		ID:            "s-1",
		Title:         "Retry Backoff",
		UpdatedAt:     now.Add(-2 * time.Hour),
		State:         "idle",
		Turns:         14,
		ContextTokens: 41_000,
	}}
	sp := newSessionPicker(th, theme.TierASCII, sessions)

	view := sp.View(th, theme.TierASCII, 100, now)
	plain := ansi.Strip(view)
	if !strings.Contains(plain, "14 turns") {
		t.Errorf("missing turn count in view:\n%s", plain)
	}
	if !strings.Contains(plain, "41k ctx") {
		t.Errorf("missing context size in view:\n%s", plain)
	}
}

// TestSessionPickerRefresh_UpdatesActiveAndStateFromLiveCheck pins the
// live-refresh contract: refresh re-derives Active/State per row from
// the supplied liveness check, keyed by session ID, and touches nothing
// else on the row (Title/Turns/ContextTokens/IsCurrent are a snapshot
// from when the picker opened, not re-queried on every tick).
func TestSessionPickerRefresh_UpdatesActiveAndStateFromLiveCheck(t *testing.T) {
	th := loadTheme(t)
	sessions := []ports.SessionSummary{
		{ID: "s-1", Title: "Was idle, now running", Active: false, State: "done", Turns: 3},
		{ID: "s-2", Title: "Was running, now idle", Active: true, State: "running", Turns: 7},
	}
	sp := newSessionPicker(th, theme.TierASCII, sessions)

	live := map[string]bool{"s-1": true, "s-2": false}
	sp = sp.refresh(func(id string) bool { return live[id] })

	if !sp.sessions[0].Active || sp.sessions[0].State != "running" {
		t.Errorf("s-1 = %+v, want Active=true State=running", sp.sessions[0])
	}
	if sp.sessions[0].Title != "Was idle, now running" || sp.sessions[0].Turns != 3 {
		t.Errorf("s-1 unrelated fields changed: %+v", sp.sessions[0])
	}
	if sp.sessions[1].Active || sp.sessions[1].State != "done" {
		t.Errorf("s-2 = %+v, want Active=false State=done", sp.sessions[1])
	}
}

// TestSessionPickerRefresh_NilActiveCheckIsNoOp guards the defensive nil
// check: a picker refreshed with no liveness function (e.g. a runner
// that predates SessionActive) must return its rows unchanged rather
// than panic.
func TestSessionPickerRefresh_NilActiveCheckIsNoOp(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sp := newSessionPicker(th, theme.TierASCII, sampleSessions(now))

	got := sp.refresh(nil)
	if !reflect.DeepEqual(got.sessions, sp.sessions) {
		t.Errorf("refresh(nil) changed sessions: got %+v, want unchanged %+v", got.sessions, sp.sessions)
	}
}

// TestSessionPickerTickCmd_EmitsSessionPickerTickMsg pins the tick
// primitive itself: it must fire a sessionPickerTickMsg, the type the
// screen's Update loop keys the live-refresh case on.
func TestSessionPickerTickCmd_EmitsSessionPickerTickMsg(t *testing.T) {
	cmd := sessionPickerTickCmd()
	if cmd == nil {
		t.Fatal("sessionPickerTickCmd returned a nil Cmd")
	}
	msg := cmd()
	if _, ok := msg.(sessionPickerTickMsg); !ok {
		t.Errorf("got %T, want sessionPickerTickMsg", msg)
	}
}

// TestSessionPickerOmitsTurnAndContextColumnsWhenZero pins the
// zero-value-safe contract SessionSummary's own doc comment states: a
// session an adapter cannot report turns/context for must not render a
// fabricated "0 turns" / "0k ctx".
func TestSessionPickerOmitsTurnAndContextColumnsWhenZero(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sessions := []ports.SessionSummary{{
		ID:        "s-1",
		Title:     "No Metadata",
		UpdatedAt: now.Add(-2 * time.Hour),
		State:     "idle",
	}}
	sp := newSessionPicker(th, theme.TierASCII, sessions)

	view := sp.View(th, theme.TierASCII, 100, now)
	plain := ansi.Strip(view)
	if strings.Contains(plain, "turns") || strings.Contains(plain, "ctx") {
		t.Errorf("expected no turns/ctx column for a zero-value session:\n%s", plain)
	}
}
