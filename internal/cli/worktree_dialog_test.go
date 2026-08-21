package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// ─── Enter switches worktree ────────────────────────────────────────────

func TestWorktreeDialogEnterSwitchFailsOnFakePath(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 2)

	// enter on fake path will fail chdir, setting an error notice.
	m.handleChatKey("enter", false)
	// Dialog should still be open (switch failed because fake path).
	if m.worktreeDlg == nil {
		t.Fatal("dialog should stay open on failed switch")
	}
	if m.worktreeDlg.notice == "" {
		t.Fatal("enter must set error notice when chdir fails")
	}
	if !strings.Contains(m.worktreeDlg.notice, "switch failed") {
		t.Fatalf("notice should mention switch failure: %q", m.worktreeDlg.notice)
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
	if layout.InnerW <= 0 || layout.PageH <= 0 {
		t.Fatalf("layout should be positive: innerW=%d pageH=%d", layout.InnerW, layout.PageH)
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
	for _, want := range []string{"move", "create", "delete", "close", "switch", "back to main"} {
		if !strings.Contains(strings.ToLower(footer), want) {
			t.Fatalf("default footer must contain %q: %q", want, footer)
		}
	}
}

// ─── Back to main tree (b key) ────────────────────────────────────────

func TestWorktreeDialogBackToMainNotGitRepo(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 2)

	// Save cwd and chdir to a temp dir (not a git repo)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := t.TempDir()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	m.handleChatKey("b", false)
	if m.worktreeDlg == nil {
		t.Fatal("dialog should stay open when RepoRoot fails")
	}
	if m.worktreeDlg.notice == "" {
		t.Fatal("b key must set error notice when not in git repo")
	}
	if !strings.Contains(m.worktreeDlg.notice, "not inside a git repo") {
		t.Fatalf("notice should mention not inside git repo: %q", m.worktreeDlg.notice)
	}
}

func TestWorktreeDialogBackToMainSuccess(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 2)

	// We're already in the git repo (tests run from project root),
	// so pressing b should switch to repo root (which is where we are),
	// close the dialog, and update context.
	m.handleChatKey("b", false)
	if m.worktreeDlg != nil {
		t.Fatalf("dialog should close on successful back-to-main, notice: %q", m.worktreeDlg.notice)
	}
}

func TestSwitchToMainTreeFromWorktree(t *testing.T) {
	// Verify that switchToMainTree resolves to the main repo root even
	// when the process cwd is inside a linked worktree.
	tmpDir := t.TempDir()
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "test")
	runGit(t, tmpDir, "commit", "--allow-empty", "-m", "initial")

	// Create a linked worktree.
	worktreePath := tmpDir + "/.mivia/worktrees/wt-1"
	runGit(t, tmpDir, "worktree", "add", worktreePath, "-b", "wt/wt-1", "HEAD")

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origDir)

	if err := os.Chdir(worktreePath); err != nil {
		t.Fatalf("chdir to worktree: %v", err)
	}

	absTmp, _ := filepath.Abs(tmpDir)

	m := newReadyChatModel(30, 90)
	m.workspaceDir = worktreePath
	openWorktreeDialogOnModel(m, 0)

	m.handleChatKey("b", false)

	if m.worktreeDlg != nil {
		t.Fatalf("dialog should close on success, notice: %q", m.worktreeDlg.notice)
	}
	if m.workspaceDir != absTmp {
		t.Fatalf("workspace = %q, want main root %q", m.workspaceDir, absTmp)
	}
	if m.restartWorkspace != absTmp {
		t.Fatalf("restart workspace = %q, want main root %q", m.restartWorkspace, absTmp)
	}
}

// ─── Async create flow (c key → worktreeCreatedMsg → applyWorktreeCreated) ───

func TestWorktreeDialogCreateReturnsCmd(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)

	// "c" key should return a tea.Cmd (not nil).
	_, _, cmds := m.handleChatKey("c", false)
	if len(cmds) == 0 || cmds[0] == nil {
		t.Fatal("c key must return a non-nil tea.Cmd")
	}
	// Dialog should be in creating state immediately.
	if !m.worktreeDlg.creating {
		t.Fatal("dialog must be in creating state after c key")
	}
	// View should show the creating placeholder.
	view := stripANSI(m.View())
	if !strings.Contains(strings.ToLower(view), "creating") {
		t.Fatalf("view must show creating placeholder after c key:\n%s", view)
	}
}

func TestWorktreeDialogCreateIgnoredWhenAlreadyCreating(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)

	// First c sets creating=true.
	_, _, cmds1 := m.handleChatKey("c", false)
	if len(cmds1) == 0 {
		t.Fatal("first c must return a cmd")
	}
	// Second c must return nil (no-op).
	_, _, cmds2 := m.handleChatKey("c", false)
	if len(cmds2) != 0 {
		t.Fatal("second c while creating must return no cmd")
	}
}

func TestWorktreeDialogCreateRefusedWhileAgentRuns(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)
	m.waiting = true

	_, _, cmds := m.handleChatKey("c", false)

	if len(cmds) != 0 {
		t.Fatal("create must not return a command while an agent runs")
	}
	if m.worktreeDlg.creating {
		t.Fatal("create must not enter the creating state while an agent runs")
	}
	if !strings.Contains(m.worktreeDlg.notice, "cannot switch while agent is running") {
		t.Fatalf("notice = %q, want busy refusal", m.worktreeDlg.notice)
	}
}

func TestWorktreeDialogCreateRefusedWhileAgentCancels(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)
	m.cancelling = true

	_, _, cmds := m.handleChatKey("c", false)

	if len(cmds) != 0 {
		t.Fatal("create must not return a command while an agent cancels")
	}
	if !strings.Contains(m.worktreeDlg.notice, "cannot switch while agent is running") {
		t.Fatalf("notice = %q, want busy refusal", m.worktreeDlg.notice)
	}
}

func TestWorktreeDialogCreateIgnoredWhenNoDialog(t *testing.T) {
	m := newReadyChatModel(30, 90)
	// No dialog open — createWorktreeFromDialog should return nil.
	cmd := m.createWorktreeFromDialog()
	if cmd != nil {
		t.Fatal("createWorktreeFromDialog must return nil when no dialog")
	}
}

func TestApplyWorktreeCreatedSuccess(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 1)
	m.worktreeDlg.creating = true

	wt := &vcs.WorktreeInfo{
		Name:   "wt-2",
		Path:   "/tmp/project/.mivia/worktrees/wt-2",
		Branch: "feature/new",
	}
	msg := worktreeCreatedMsg{wt: wt, instance: testWorktreeInstance(wt.Name), err: nil, dlg: m.worktreeDlg}
	m.applyWorktreeCreated(msg)

	if m.worktreeDlg.creating {
		t.Fatal("creating must be false after applyWorktreeCreated")
	}
	if len(m.worktreeDlg.worktrees) != 2 {
		t.Fatalf("worktree count should be 2, got %d", len(m.worktreeDlg.worktrees))
	}
	if m.worktreeDlg.worktrees[1].Name != "wt-2" {
		t.Fatalf("new worktree name = %q, want wt-2", m.worktreeDlg.worktrees[1].Name)
	}
	if m.worktreeDlg.cursor != 1 {
		t.Fatalf("cursor should select new worktree (1), got %d", m.worktreeDlg.cursor)
	}
	if !strings.Contains(m.worktreeDlg.notice, "created") {
		t.Fatalf("notice should mention created: %q", m.worktreeDlg.notice)
	}
	// Dialog must still be open.
	if m.worktreeDlg == nil {
		t.Fatal("dialog must stay open after successful create")
	}
}

func TestApplyWorktreeCreatedError(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)
	m.worktreeDlg.creating = true

	msg := worktreeCreatedMsg{
		wt:  nil,
		err: errors.New("git worktree add: permission denied"),
		dlg: m.worktreeDlg,
	}
	m.applyWorktreeCreated(msg)

	if m.worktreeDlg.creating {
		t.Fatal("creating must be false after error")
	}
	if len(m.worktreeDlg.worktrees) != 0 {
		t.Fatalf("worktree list must stay empty on error, got %d", len(m.worktreeDlg.worktrees))
	}
	if !strings.Contains(m.worktreeDlg.notice, "create failed") {
		t.Fatalf("notice should mention create failed: %q", m.worktreeDlg.notice)
	}
	if !strings.Contains(m.worktreeDlg.notice, "permission denied") {
		t.Fatalf("notice should include error detail: %q", m.worktreeDlg.notice)
	}
	// Dialog must still be open.
	if m.worktreeDlg == nil {
		t.Fatal("dialog must stay open after create error")
	}
}

func TestApplyWorktreeCreatedDialogClosed(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)
	m.worktreeDlg.creating = true

	// User closes the dialog while creation is in-flight.
	m.worktreeDlg = nil

	// Message arrives for a nil dialog — must not panic.
	msg := worktreeCreatedMsg{
		wt:  &vcs.WorktreeInfo{Name: "wt-1"},
		err: nil,
	}
	m.applyWorktreeCreated(msg) // must not panic
}

func TestApplyWorktreeCreatedSuccessRendersInUpdate(t *testing.T) {
	m := newReadyChatModel(30, 90)
	openWorktreeDialogOnModel(m, 0)

	// Simulate the full flow: key → cmd → Update(msg) → View.
	_, _, cmds := m.handleChatKey("c", false)
	if len(cmds) == 0 {
		t.Fatal("c key must return a cmd")
	}

	// The cmd is createWorktreeAsync which calls vcs.Create.
	// We can't control the real git repo in tests, but we can test that
	// applyWorktreeCreated produces the right view output.
	// Manually set creating state (normally done by the key handler).
	m.worktreeDlg.creating = true
	m.hitMap.invalidate()

	// Verify "creating…" is visible.
	viewBefore := stripANSI(m.View())
	if !strings.Contains(strings.ToLower(viewBefore), "creating") {
		t.Fatalf("before: view must show creating placeholder:\n%s", viewBefore)
	}

	// Now deliver the result as if bubbletea routed the message.
	msg := worktreeCreatedMsg{
		wt: &vcs.WorktreeInfo{
			Name:   "wt-1",
			Path:   "/tmp/project/.mivia/worktrees/wt-1",
			Branch: "main",
		},
		instance: testWorktreeInstance("wt-1"),
		err:      nil,
		dlg:      m.worktreeDlg,
	}
	m.applyWorktreeCreated(msg)

	viewAfter := stripANSI(m.View())
	if strings.Contains(strings.ToLower(viewAfter), "creating") {
		t.Fatalf("after: view must NOT show creating placeholder:\n%s", viewAfter)
	}
	if !strings.Contains(viewAfter, "wt-1") {
		t.Fatalf("after: view must show new worktree name:\n%s", viewAfter)
	}
	// The notice may be truncated by dialog width, so verify it on the model
	// directly rather than requiring it in the rendered view.
	if !strings.Contains(strings.ToLower(m.worktreeDlg.notice), "created") {
		t.Fatalf("notice must mention created: %q", m.worktreeDlg.notice)
	}
}

func TestCreateWorktreeAsyncDeliversMessage(t *testing.T) {
	m := newReadyChatModel(30, 90)

	// createWorktreeAsync returns a tea.Cmd — invoke it to get the message.
	cmd := m.createWorktreeAsync("/nonexistent/repo", "wt-99", nil)
	if cmd == nil {
		t.Fatal("createWorktreeAsync must return non-nil cmd")
	}
	msg := cmd()
	wtMsg, ok := msg.(worktreeCreatedMsg)
	if !ok {
		t.Fatalf("cmd must return worktreeCreatedMsg, got %T", msg)
	}
	// /nonexistent/repo is not a git repo, so err should be set.
	if wtMsg.err == nil {
		t.Fatal("create on nonexistent repo must return error")
	}
}

func TestCreateWorktreeAsyncSuccessOnRealRepo(t *testing.T) {
	// Create a real temporary git repo, then test creation through it.
	tmpDir := t.TempDir()

	// Init a bare repo.
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "test")
	runGit(t, tmpDir, "commit", "--allow-empty", "-m", "initial")

	m := newReadyChatModel(30, 90)
	m.workspaceDir = tmpDir

	cmd := m.createWorktreeAsync(tmpDir, "real-wt", nil)
	msg := cmd()
	wtMsg, ok := msg.(worktreeCreatedMsg)
	if !ok {
		t.Fatalf("cmd must return worktreeCreatedMsg, got %T", msg)
	}
	if wtMsg.err != nil {
		t.Fatalf("create should succeed on real repo: %v", wtMsg.err)
	}
	if wtMsg.wt == nil {
		t.Fatal("wt must not be nil on success")
	}
	if wtMsg.wt.Name != "real-wt" {
		t.Fatalf("wt.Name = %q, want real-wt", wtMsg.wt.Name)
	}
	if !strings.Contains(wtMsg.wt.Path, "real-wt") {
		t.Fatalf("wt.Path = %q, must contain real-wt", wtMsg.wt.Path)
	}
}

func TestCreateWorktreeAsyncDuplicateName(t *testing.T) {
	tmpDir := t.TempDir()

	// Init repo + create first worktree.
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "test")
	runGit(t, tmpDir, "commit", "--allow-empty", "-m", "initial")
	runGit(t, tmpDir, "worktree", "add", tmpDir+"/.mivia/worktrees/dup", "-b", "wt/dup", "HEAD")

	m := newReadyChatModel(30, 90)
	cmd := m.createWorktreeAsync(tmpDir, "dup", nil)
	msg := cmd()
	wtMsg := msg.(worktreeCreatedMsg)
	if wtMsg.err == nil {
		t.Fatal("duplicate worktree name must return error")
	}
}

func TestWorktreeDialogCreateFromWorktreePath(t *testing.T) {
	// Simulate the scenario where workspaceDir points to a worktree path
	// rather than the main repo root. The create flow must still resolve
	// to the correct repo root.
	tmpDir := t.TempDir()

	// Init a real repo.
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "test")
	runGit(t, tmpDir, "commit", "--allow-empty", "-m", "initial")

	// Create a linked worktree to use as the "cwd" path.
	worktreePath := tmpDir + "/.mivia/worktrees/wt-existing"
	runGit(t, tmpDir, "worktree", "add", worktreePath, "-b", "wt/wt-existing", "HEAD")

	m := newReadyChatModel(30, 90)
	// Point workspaceDir at the worktree (the problematic scenario).
	m.workspaceDir = worktreePath

	// resolveRepoRoot should resolve to the main repo, not the worktree.
	mainRoot := m.resolveRepoRoot()
	if mainRoot == worktreePath {
		t.Fatalf("resolveRepoRoot returned worktree path %q, want main root", mainRoot)
	}
	// The returned root should be the tmpDir (main repo).
	absTmp, _ := filepath.Abs(tmpDir)
	if mainRoot != absTmp {
		t.Fatalf("resolveRepoRoot = %q, want main root %q", mainRoot, absTmp)
	}

	// Now verify the async create uses the correct root by actually creating
	// a worktree through the model.
	cmd := m.createWorktreeAsync(mainRoot, "wt-created-from-wt", nil)
	msg := cmd()
	wtMsg, ok := msg.(worktreeCreatedMsg)
	if !ok {
		t.Fatalf("expected worktreeCreatedMsg, got %T", msg)
	}
	if wtMsg.err != nil {
		t.Fatalf("create from worktree path should succeed: %v", wtMsg.err)
	}
	if wtMsg.wt == nil || wtMsg.wt.Name != "wt-created-from-wt" {
		t.Fatalf("unexpected result: %+v", wtMsg)
	}
}

// runGit runs a git command in the given directory. Fails the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", cmd...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}
