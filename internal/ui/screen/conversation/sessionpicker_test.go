package conversation

import (
	"fmt"
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

// worktreeSampleSessions mixes plain, worktree-bound, and route rows in
// adapter order: one real session bound to wt1 plus the synthesized
// "start a new session here" pseudo-row for an uncovered wt2.
func worktreeSampleSessions(now time.Time) []ports.SessionSummary {
	return []ports.SessionSummary{
		{
			ID:        "s-plain",
			Title:     "Main Session",
			UpdatedAt: now.Add(-5 * time.Minute),
			State:     "done",
		},
		{
			ID:          "bound-wt1",
			Title:       "Worktree Work",
			UpdatedAt:   now.Add(-2 * time.Hour),
			State:       "done",
			Turns:       4,
			Worktree:    "wt1",
			WorktreeDir: "/repo/.mivia/worktrees/wt1",
		},
		{
			ID:            "worktree:wt2",
			Title:         "Worktree · wt2",
			UpdatedAt:     now.Add(-3 * time.Hour),
			State:         "done",
			Worktree:      "wt2",
			WorktreeRoute: true,
			WorktreeDir:   "/repo/.mivia/worktrees/wt2",
		},
	}
}

// TestSessionPickerSinksRouteRowsUnderSeparator pins the grouping: route
// pseudo-rows sort below every real session so ViewWindow can draw them
// under one "-- in worktree --" line without splitting cursor ranges.
func TestSessionPickerSinksRouteRowsUnderSeparator(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sp := newSessionPicker(th, theme.TierTrueColor, worktreeSampleSessions(now))

	order := []string{}
	for _, s := range sp.visible() {
		order = append(order, s.ID)
	}
	want := []string{"s-plain", "bound-wt1", "worktree:wt2"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("visible order = %v, want %v", order, want)
	}

	view := ansi.Strip(sp.View(th, theme.TierTrueColor, 120, now))
	if !strings.Contains(view, "-- in worktree --") {
		t.Errorf("expected route separator in view:\n%s", view)
	}
	sepIdx := strings.Index(view, "-- in worktree --")
	routeIdx := strings.Index(view, "Worktree · wt2")
	if sepIdx == -1 || routeIdx == -1 || routeIdx < sepIdx {
		t.Errorf("route row must render after its separator:\n%s", view)
	}

	// The branch glyph marks both kinds of worktree row and no plain row.
	glyphCount := strings.Count(view, "⎇")
	if glyphCount != 2 {
		t.Errorf("glyph count = %d, want one per worktree row (2):\n%s", glyphCount, view)
	}
}

// TestSessionPickerWorktreeGlyphASCII pins the ASCII-tier fallback: tiers
// that cannot draw ⎇ degrade to "+ " instead of losing the marker.
func TestSessionPickerWorktreeGlyphASCII(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sessions := []ports.SessionSummary{{
		ID:            "worktree:wt1",
		Title:         "Worktree · wt1",
		UpdatedAt:     now.Add(-time.Hour),
		Worktree:      "wt1",
		WorktreeRoute: true,
		WorktreeDir:   "/repo/.mivia/worktrees/wt1",
	}}
	sp := newSessionPicker(th, theme.TierASCII, sessions)

	view := ansi.Strip(sp.View(th, theme.TierASCII, 100, now))
	if strings.Contains(view, "+ ") && strings.Contains(view, "Worktree · wt1") {
		return
	}
	t.Errorf("ASCII-tier row lost its glyph fallback:\n%s", view)
}

// TestSessionPickerPreviewShowsWorktreeDetail pins the right-pane detail:
// selecting a worktree row surfaces its name and directory.
func TestSessionPickerPreviewShowsWorktreeDetail(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sessions := worktreeSampleSessions(now)
	sp := newSessionPicker(th, theme.TierTrueColor, sessions)

	view := ansi.Strip(sp.PreviewView(th, theme.TierTrueColor, 100, 24, now))
	if strings.Contains(view, "Worktree:") {
		t.Errorf("plain session must not show a worktree detail:\n%s", view)
	}

	sp.cursor = 1 // bound-wt1
	view = ansi.Strip(sp.PreviewView(th, theme.TierTrueColor, 100, 24, now))
	if !strings.Contains(view, "Worktree: wt1 (/repo/.mivia/worktrees/wt1)") {
		t.Errorf("bound-session preview missing worktree detail:\n%s", view)
	}
}

// TestSessionPickerFilterKeepsMatchingRouteRows guards the filter rule:
// route rows are not special-cased out of filtering; typing a worktree's
// name narrows to it and keeps routes last.
func TestSessionPickerFilterKeepsMatchingRouteRows(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sp := newSessionPicker(th, theme.TierTrueColor, worktreeSampleSessions(now))

	sp, _ = sp.Update(tea.KeyPressMsg{Text: "w"})
	if len(sp.visible()) != 2 { // Worktree Work + Worktree · wt2
		t.Fatalf("filtered count = %d, want 2:\n%+v", len(sp.visible()), sp.visible())
	}
	last := sp.visible()[len(sp.visible())-1]
	if !last.WorktreeRoute {
		t.Fatalf("route row not kept last under filter: %+v", sp.visible())
	}
}

// TestPickSelectionCmd_DispatchesByRowKind pins the enter-key payload
// split: worktree-flavored rows emit resumePickMsg with the full summary;
// plain rows keep the id-only picker.SelectMsg.
func TestPickSelectionCmd_DispatchesByRowKind(t *testing.T) {
	bound := ports.SessionSummary{
		ID:                 "bound-wt1",
		Title:              "Worktree Work",
		Worktree:           "wt1",
		WorktreeInstanceID: "wt_0000000000000001",
	}
	msg := pickSelectionCmd(bound)()
	if _, ok := msg.(resumePickMsg); !ok {
		t.Fatalf("bound row produced %T, want resumePickMsg", msg)
	}

	// A row with worktree metadata but NO managed instance (legacy
	// pre-instance sessions, sessions saved from an unadopted worktree
	// dir) must resume through the PLAIN path: the scoped creator would
	// fail on LiveWorktreeInstance for a worktree storage never tracked.
	legacy := ports.SessionSummary{
		ID:       "legacy-1",
		Title:    "Old Worktree Session",
		Worktree: "wt1",
	}
	msg = pickSelectionCmd(legacy)()
	if sel, ok := msg.(picker.SelectMsg); !ok || sel.Item != legacy.ID {
		t.Fatalf("instance-less worktree row produced %T, want picker.SelectMsg", msg)
	}

	route := ports.SessionSummary{
		ID:            "worktree:wt2",
		Title:         "Worktree · wt2",
		Worktree:      "wt2",
		WorktreeRoute: true,
	}
	msg = pickSelectionCmd(route)()
	if rp, ok := msg.(resumePickMsg); !ok || rp.summary.ID != route.ID {
		t.Fatalf("route row produced %T (%+v), want resumePickMsg", msg, msg)
	}

	plain := ports.SessionSummary{ID: "s-plain", Title: "Main"}
	msg = pickSelectionCmd(plain)()
	if sel, ok := msg.(picker.SelectMsg); !ok || sel.Item != plain.ID {
		t.Fatalf("plain row produced %T, want picker.SelectMsg", msg)
	}
}

// TestSessionPickerPreviewDirOnlyWorktreeRow pins the graceful fallback:
// a row carrying only WorktreeDir names the worktree from the directory.
func TestSessionPickerPreviewDirOnlyWorktreeRow(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sessions := []ports.SessionSummary{{
		ID:          "dir-only",
		Title:       "Legacy Row",
		UpdatedAt:   now.Add(-time.Hour),
		WorktreeDir: "/repo/.mivia/worktrees/wt9",
	}}
	sp := newSessionPicker(th, theme.TierTrueColor, sessions)

	view := ansi.Strip(sp.PreviewView(th, theme.TierTrueColor, 100, 24, now))
	if !strings.Contains(view, "Worktree: wt9 (/repo/.mivia/worktrees/wt9)") {
		t.Errorf("dir-only row lost its base-name fallback:\n%s", view)
	}
}

// TestSessionPickerPreviewScrollNoteAndDialogFallbacks cover the remaining
// render branches the worktree split touched: the scroll-note line on
// transcript-heavy previews and the unbounded-height dialog fallback.
func TestSessionPickerPreviewScrollNoteAndDialogFallbacks(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)

	heavy := ports.SessionSummary{
		ID: "s-heavy", Title: "Long Session", UpdatedAt: now.Add(-time.Hour), State: "done",
	}
	for i := range 40 {
		heavy.Lines = append(heavy.Lines, fmt.Sprintf("line %d", i))
	}
	sp := newSessionPicker(th, theme.TierTrueColor, []ports.SessionSummary{heavy})

	preview := ansi.Strip(sp.PreviewView(th, theme.TierTrueColor, 100, 8, now))
	if !strings.Contains(preview, "[1-5 of 40 lines]") {
		t.Errorf("preview missing scroll note:\n%s", preview)
	}

	// Detail line present (worktree row) with offset scrolled: exercises
	// the widened fixed budget and the truncation branch on long lines.
	wide := ports.SessionSummary{
		ID: "s-wide", Title: "Wide Worktree Row", UpdatedAt: now.Add(-time.Hour),
		State:       "done",
		Worktree:    "wtX",
		WorktreeDir: "/repo/wt/x",
	}
	for i := range 12 {
		wide.Lines = append(wide.Lines, "> "+fmt.Sprintf("%09d ", i)+strings.Repeat("x", 200))
	}
	sp2 := newSessionPicker(th, theme.TierTrueColor, []ports.SessionSummary{wide})
	sp2.cursor = 0
	sp2.previewOffset = 3
	preview2 := ansi.Strip(sp2.PreviewView(th, theme.TierTrueColor, 40, 8, now))
	if !strings.Contains(preview2, "Worktree: wtX (/repo/wt/x)") {
		t.Errorf("wide preview lost its worktree detail:\n%s", preview2)
	}
	if preview2 == "" {
		t.Fatal("empty wide preview")
	}

	dialog := ansi.Strip(renderSessionPickerDialog(th, theme.TierTrueColor, 100, 0, sp, now))
	if !strings.Contains(dialog, "resume session") || !strings.Contains(dialog, "Long Session") {
		t.Errorf("height=0 dialog lost its chrome or body:\n%s", dialog)
	}
}

// TestSessionPickerEmptyTitleFallsBackToIDAndPreviewNeedsSelection pin two
// remaining render fallbacks touched by the worktree split: a row without
// a title renders its ID (after the glyph), and an empty picker's preview
// reports the absence instead of panicking.
func TestSessionPickerEmptyTitleFallsBackToIDAndPreviewNeedsSelection(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)

	sessions := []ports.SessionSummary{{
		ID:            "worktree:wtn",
		Title:         "",
		UpdatedAt:     now.Add(-time.Hour),
		Worktree:      "wtn",
		WorktreeRoute: true,
	}}
	sp := newSessionPicker(th, theme.TierTrueColor, sessions)
	view := ansi.Strip(sp.View(th, theme.TierTrueColor, 100, now))
	if !strings.Contains(view, "⎇ worktree:wtn") {
		t.Errorf("empty-title row did not fall back to its ID with the glyph:\n%s", view)
	}

	empty := newSessionPicker(th, theme.TierTrueColor, nil)
	pv := ansi.Strip(empty.PreviewView(th, theme.TierTrueColor, 100, 20, now))
	if !strings.Contains(pv, "no session selected") {
		t.Errorf("empty picker preview = %q, want the no-selection notice", pv)
	}
}

// TestSessionPickerFilteredWindowDrawsFooter pins the filter-footer branch
// under a bounded window together with the route-block reservation - the
// exact combination where the first geometry fix over-budgeted.
func TestSessionPickerFilteredWindowDrawsFooter(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sp := newSessionPicker(th, theme.TierTrueColor, worktreeSampleSessions(now))

	sp, _ = sp.Update(tea.KeyPressMsg{Text: "w"})
	view := ansi.Strip(sp.ViewWindow(th, theme.TierTrueColor, 100, 6, now))

	if !strings.Contains(view, "/w") {
		t.Errorf("filtered window lost its query footer:\n%s", view)
	}
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) > 6 {
		t.Fatalf("filtered window drew %d lines in a 6-row budget:\n%s", len(lines), view)
	}
}

// TestSessionPickerEmptyViews pin the two empty-state renders of the
// bounded window: filter mismatch and a wholly empty list.
func TestSessionPickerEmptyViews(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)

	sp := newSessionPicker(th, theme.TierTrueColor, worktreeSampleSessions(now))
	sp, _ = sp.Update(tea.KeyPressMsg{Text: "zz"})
	if view := ansi.Strip(sp.ViewWindow(th, theme.TierTrueColor, 100, 6, now)); !strings.Contains(view, "no sessions match /zz") {
		t.Errorf("filter-mismatch empty state = %q", view)
	}

	empty := newSessionPicker(th, theme.TierTrueColor, nil)
	view := ansi.Strip(empty.ViewWindow(th, theme.TierTrueColor, 100, 6, now))
	if !strings.Contains(view, "no saved sessions found") {
		t.Errorf("empty-list state = %q", view)
	}
}

// TestSessionPickerFailedAndStreamingRowsRender pin the status-mark and
// badge variants (failed, streaming) on worktree-flavored rows so the
// split-out renderer keeps all four lifecycle states rendering.
func TestSessionPickerFailedAndStreamingRowsRender(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sessions := []ports.SessionSummary{
		{ID: "s-fail", Title: "Boom", UpdatedAt: now.Add(-time.Hour), State: "failed", Worktree: "wf"},
		{ID: "s-run", Title: "Streaming", UpdatedAt: now.Add(-2 * time.Hour), Active: true, State: "streaming", Turns: 3, ContextTokens: 41000},
	}
	sp := newSessionPicker(th, theme.TierTrueColor, sessions)
	view := ansi.Strip(sp.View(th, theme.TierTrueColor, 100, now))
	for _, want := range []string{"ERR", "41k ctx", "Streaming"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

// TestSessionPickerCurrentAndASCIIFailedBadges pin the last two badge/meta
// variants: the "current" tag on an IsCurrent row's context segment and
// the ASCII-tier failed badge label.
func TestSessionPickerCurrentAndASCIIFailedBadges(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	th := loadTheme(t)
	sessions := []ports.SessionSummary{
		{ID: "s-cur", Title: "Here", UpdatedAt: now.Add(-time.Hour), State: "done",
			Turns: 1, ContextTokens: 2000, IsCurrent: true},
		{ID: "s-bad", Title: "Ascii Fail", UpdatedAt: now.Add(-2 * time.Hour), State: "failed", Worktree: "wf"},
	}
	sp := newSessionPicker(th, theme.TierASCII, sessions)
	view := ansi.Strip(sp.View(th, theme.TierASCII, 100, now))
	for _, want := range []string{"current", "2k ctx", "x ERR"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}
