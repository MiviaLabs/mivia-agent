package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// ─── Helpers ────────────────────────────────────────────────────────────

func seedWorktrees(m *tuiModel, n int) {
	m.worktreeDlg = nil
	var wts []vcs.WorktreeInfo
	for i := 0; i < n; i++ {
		wts = append(wts, vcs.WorktreeInfo{
			Name:   fmt.Sprintf("wt-%d", i),
			Branch: fmt.Sprintf("feature/branch-%d", i),
			Path:   fmt.Sprintf("/tmp/project/.mivia/worktrees/wt-%d", i),
		})
	}
	m.worktreeDlg = newWorktreeDialog(wts)
	m.hitMap.invalidate()
}

// openWorktreeDialogOnModel creates and opens the worktree dialog with fake
// worktree data, bypassing vcs.List (which needs a real git repo).
func openWorktreeDialogOnModel(m *tuiModel, n int) {
	seedWorktrees(m, n)
}

// ─── Dialog open & close ───────────────────────────────────────────────

func TestWorktreeDialogOpensAndLists(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 4)

	if m.worktreeDlg == nil {
		t.Fatal("/worktrees must open the dialog")
	}
	view := stripANSI(m.View())
	for i := 0; i < 4; i++ {
		if !strings.Contains(view, fmt.Sprintf("wt-%d", i)) {
			t.Fatalf("dialog missing wt-%d:\n%s", i, view)
		}
	}
	if !strings.Contains(view, "4") {
		t.Fatalf("dialog header must show count:\n%s", view)
	}
	for _, want := range []string{"create", "delete"} {
		if !strings.Contains(strings.ToLower(view), want) {
			t.Fatalf("dialog must advertise %q:\n%s", want, view)
		}
	}
}

func TestWorktreeDialogEmptyState(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)

	if m.worktreeDlg == nil {
		t.Fatal("dialog must open even when empty")
	}
	view := stripANSI(m.View())
	if !strings.Contains(strings.ToLower(view), "no worktrees") {
		t.Fatalf("empty dialog must say so:\n%s", view)
	}
	// Destructive keys must be inert on an empty list.
	m.handleChatKey("d", false)
	if m.worktreeDlg.confirm != wtConfirmNone {
		t.Fatal("d must be inert when list is empty")
	}
}

// ─── Navigation ─────────────────────────────────────────────────────────

func TestWorktreeDialogNavigateAndClose(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 3)

	if m.worktreeDlg.cursor != 0 {
		t.Fatalf("cursor starts at %d, want 0", m.worktreeDlg.cursor)
	}
	// down moves.
	m.handleChatKey("down", false)
	if m.worktreeDlg.cursor != 1 {
		t.Fatalf("down: cursor=%d, want 1", m.worktreeDlg.cursor)
	}
	// j also moves.
	m.handleChatKey("j", false)
	if m.worktreeDlg.cursor != 2 {
		t.Fatalf("j: cursor=%d, want 2", m.worktreeDlg.cursor)
	}
	// Clamp at bottom.
	m.handleChatKey("down", false)
	if m.worktreeDlg.cursor != 2 {
		t.Fatalf("clamp bottom: cursor=%d, want 2", m.worktreeDlg.cursor)
	}
	// up / k back to top.
	m.handleChatKey("up", false)
	m.handleChatKey("up", false)
	if m.worktreeDlg.cursor != 0 {
		t.Fatalf("back to top: cursor=%d, want 0", m.worktreeDlg.cursor)
	}
	m.handleChatKey("k", false)
	if m.worktreeDlg.cursor != 0 {
		t.Fatalf("k clamp top: cursor=%d, want 0", m.worktreeDlg.cursor)
	}
	// Escape closes.
	m.handleChatKey("esc", false)
	if m.worktreeDlg != nil {
		t.Fatal("esc must close the dialog")
	}
}

func TestWorktreeDialogQAlsoCloses(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 2)

	m.handleChatKey("q", false)
	if m.worktreeDlg != nil {
		t.Fatal("q must close the dialog")
	}
}

// ─── Enter shows path ──────────────────────────────────────────────────

func TestWorktreeDialogEnterCopiesPath(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 2)

	if m.worktreeDlg.notice != "" {
		t.Fatalf("notice should start empty, got %q", m.worktreeDlg.notice)
	}
	m.handleChatKey("enter", false)
	if m.worktreeDlg.notice == "" {
		t.Fatal("enter must set notice to the worktree path")
	}
	if !strings.Contains(m.worktreeDlg.notice, "wt-0") {
		t.Fatalf("notice should contain worktree name: %q", m.worktreeDlg.notice)
	}
}

// ─── Delete confirmation ───────────────────────────────────────────────

func TestWorktreeDialogDeleteRequiresConfirmation(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 3)

	m.handleChatKey("d", false)
	if m.worktreeDlg.confirm != wtConfirmDelete {
		t.Fatalf("d must arm delete confirmation, got %v", m.worktreeDlg.confirm)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "wt-0") {
		t.Fatalf("confirmation must name the target:\n%s", view)
	}
	// n cancels.
	m.handleChatKey("n", false)
	if m.worktreeDlg.confirm != wtConfirmNone {
		t.Fatal("n must cancel confirmation")
	}
	if len(m.worktreeDlg.worktrees) != 3 {
		t.Fatalf("cancelled delete removed worktree: %d left", len(m.worktreeDlg.worktrees))
	}
}

func TestWorktreeDialogDeleteConfirmEscCancels(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 3)

	m.handleChatKey("d", false)
	m.handleChatKey("esc", false)
	if m.worktreeDlg.confirm != wtConfirmNone {
		t.Fatal("esc must cancel confirmation, not close dialog")
	}
	if m.worktreeDlg == nil {
		t.Fatal("esc during confirmation must keep dialog open")
	}
}

// ─── Cursor clamping after delete ───────────────────────────────────────

func TestWorktreeDialogCursorClampsAfterRemove(t *testing.T) {
	d := newWorktreeDialog([]vcs.WorktreeInfo{
		{Name: "a", Branch: "main", Path: "/a"},
		{Name: "b", Branch: "main", Path: "/b"},
	})
	d.cursor = 1
	d.removeAt(1)
	if len(d.worktrees) != 1 {
		t.Fatalf("row not removed: %d", len(d.worktrees))
	}
	if d.cursor != 0 {
		t.Fatalf("cursor must clamp to 0, got %d", d.cursor)
	}
	d.removeAt(0)
	if len(d.worktrees) != 0 || d.cursor != 0 {
		t.Fatalf("empty state wrong: n=%d cursor=%d", len(d.worktrees), d.cursor)
	}
}

func TestWorktreeDialogRemoveAtInvalidIndex(t *testing.T) {
	d := newWorktreeDialog([]vcs.WorktreeInfo{
		{Name: "a", Branch: "main", Path: "/a"},
	})
	d.removeAt(-1)
	d.removeAt(99)
	if len(d.worktrees) != 1 {
		t.Fatalf("out-of-bounds remove must be no-op: %d", len(d.worktrees))
	}
}

// ─── Move on empty list ─────────────────────────────────────────────────

func TestWorktreeDialogMoveOnEmpty(t *testing.T) {
	d := newWorktreeDialog(nil)
	d.move(1)
	d.move(-1)
	if d.cursor != 0 {
		t.Fatalf("move on empty must keep cursor at 0: %d", d.cursor)
	}
}

// ─── Scroll clamping ───────────────────────────────────────────────────

func TestWorktreeDialogScrollClamp(t *testing.T) {
	d := newWorktreeDialog([]vcs.WorktreeInfo{
		{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"},
	})
	d.cursor = 4
	d.clampScrollTo(3) // visible=3
	if d.scroll != 2 {
		t.Fatalf("scroll=%d, want 2 (cursor 4, visible 3)", d.scroll)
	}
	// Move to top: scroll should follow.
	d.cursor = 0
	d.clampScrollTo(3)
	if d.scroll != 0 {
		t.Fatalf("scroll=%d, want 0 after moving to top", d.scroll)
	}
	// visible <= 0 is promoted to 1.
	d.clampScrollTo(0)
	if d.scroll != 0 {
		t.Fatalf("scroll=%d, want 0 with zero visible", d.scroll)
	}
}

// ─── selected() ─────────────────────────────────────────────────────────

func TestWorktreeDialogSelected(t *testing.T) {
	d := newWorktreeDialog([]vcs.WorktreeInfo{
		{Name: "a", Branch: "main", Path: "/a"},
		{Name: "b", Branch: "dev", Path: "/b"},
	})
	d.cursor = 1
	wt, ok := d.selected()
	if !ok || wt.Name != "b" {
		t.Fatalf("selected: got %q ok=%v, want b", wt.Name, ok)
	}
	// Out of bounds.
	d.cursor = 99
	_, ok = d.selected()
	if ok {
		t.Fatal("selected must return false for out-of-bounds cursor")
	}
}

// ─── cursorRows ─────────────────────────────────────────────────────────

func TestWorktreeDialogCursorRows(t *testing.T) {
	d := newWorktreeDialog([]vcs.WorktreeInfo{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	})
	// At the last row, all rows are visible.
	d.cursor = 2
	if got := d.cursorRows(3); got != 3 {
		t.Fatalf("cursorRows at last item, visible=3: got %d, want 3", got)
	}
	// Not at last row: one row reserved for scroll indicator.
	d.cursor = 0
	if got := d.cursorRows(3); got != 2 {
		t.Fatalf("cursorRows mid-list, visible=3: got %d, want 2", got)
	}
	// visible=1: always 1.
	if got := d.cursorRows(1); got != 1 {
		t.Fatalf("cursorRows visible=1: got %d, want 1", got)
	}
}

// ─── ViewAt produces output ────────────────────────────────────────────

func TestWorktreeDialogViewAtRendersTitle(t *testing.T) {
	d := newWorktreeDialog([]vcs.WorktreeInfo{
		{Name: "a"}, {Name: "b"},
	})
	view, layout := d.ViewAt(80, 24)
	if layout.innerW <= 0 || layout.pageH <= 0 {
		t.Fatalf("layout should be positive: innerW=%d pageH=%d", layout.innerW, layout.pageH)
	}
	clean := stripANSI(view)
	if !strings.Contains(clean, "worktrees") {
		t.Fatalf("view must contain title:\n%s", clean)
	}
	if !strings.Contains(clean, "2") {
		t.Fatalf("view must show count:\n%s", clean)
	}
}

func TestWorktreeDialogViewAtZeroSize(t *testing.T) {
	d := newWorktreeDialog([]vcs.WorktreeInfo{{Name: "a"}})
	view, _ := d.ViewAt(0, 0)
	if view != "" {
		t.Fatalf("zero-size must return empty string, got %q", view)
	}
}

// ─── Creating state ────────────────────────────────────────────────────

func TestWorktreeDialogCreatingShowsMessage(t *testing.T) {
	d := newWorktreeDialog(nil)
	d.creating = true
	rows := d.rowLines(70, 10)
	if len(rows) == 0 {
		t.Fatal("creating must produce rows")
	}
	clean := stripANSI(rows[0])
	if !strings.Contains(strings.ToLower(clean), "creating") {
		t.Fatalf("creating row must say so: %q", clean)
	}
}

// ─── Notice in footer ───────────────────────────────────────────────────

func TestWorktreeDialogNoticeInFooter(t *testing.T) {
	d := newWorktreeDialog(nil)
	d.notice = "something happened"
	footer := stripANSI(d.footer())
	if !strings.Contains(footer, "something happened") {
		t.Fatalf("notice must appear in footer: %q", footer)
	}
}

func TestWorktreeDialogDefaultFooter(t *testing.T) {
	d := newWorktreeDialog(nil)
	footer := stripANSI(d.footer())
	for _, want := range []string{"move", "create", "delete", "close"} {
		if !strings.Contains(strings.ToLower(footer), want) {
			t.Fatalf("default footer must contain %q: %q", want, footer)
		}
	}
}
