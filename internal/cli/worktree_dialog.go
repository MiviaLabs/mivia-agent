// Worktree manager dialog (/worktrees).
//
// Lists, creates, and deletes git worktrees managed by mivia under
// .mivia/worktrees. Follows the same structural pattern as sessions_dialog.go.
package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type worktreeConfirm int

const (
	wtConfirmNone worktreeConfirm = iota
	wtConfirmDelete
)

type worktreeDialog struct {
	worktrees []vcs.WorktreeInfo
	cursor    int
	scroll    int
	confirm   worktreeConfirm
	notice    string
	creating  bool
}

func newWorktreeDialog(worktrees []vcs.WorktreeInfo) *worktreeDialog {
	return &worktreeDialog{worktrees: append([]vcs.WorktreeInfo(nil), worktrees...)}
}

func (d *worktreeDialog) removeAt(i int) {
	if i < 0 || i >= len(d.worktrees) {
		return
	}
	d.worktrees = append(d.worktrees[:i], d.worktrees[i+1:]...)
	if d.cursor >= len(d.worktrees) {
		d.cursor = max(0, len(d.worktrees)-1)
	}
	if len(d.worktrees) == 0 {
		d.cursor = 0
	}
	d.clampScroll()
}

func (d *worktreeDialog) move(delta int) {
	if len(d.worktrees) == 0 {
		return
	}
	d.cursor = min(len(d.worktrees)-1, max(0, d.cursor+delta))
	d.clampScroll()
}

func (d *worktreeDialog) clampScroll(visible ...int) {
	v := d.visibleRows(80, 24)
	if len(visible) > 0 {
		v = visible[0]
	}
	d.clampScrollTo(v)
}

func (d *worktreeDialog) clampScrollTo(visible int) {
	visible = max(1, visible)
	if d.cursor < d.scroll {
		d.scroll = d.cursor
	}
	if d.cursor >= d.scroll+visible {
		d.scroll = d.cursor - visible + 1
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
}

func worktreeDialogPrefs() dialogPrefs {
	return dialogPrefs{preferredW: 70, minW: 40, minH: 8, frameCols: 4, frameRows: 3}
}

func (d *worktreeDialog) layout(w, h int) dialogLayout {
	return makeDialogLayout(w, h, worktreeDialogPrefs(), func(innerW int) (int, int) {
		rows := d.rowLines(innerW, len(d.worktrees)+1)
		return maxWorktreeRowWidth(rows), len(rows)
	})
}

func (d *worktreeDialog) visibleRows(w, h int) int {
	l := d.layout(w, h)
	return max(1, l.pageH)
}

func (d *worktreeDialog) selected() (vcs.WorktreeInfo, bool) {
	if d.cursor < 0 || d.cursor >= len(d.worktrees) {
		return vcs.WorktreeInfo{}, false
	}
	return d.worktrees[d.cursor], true
}

func (d *worktreeDialog) ViewAt(w, h int) (string, dialogLayout) {
	l := d.layout(w, h)
	d.clampScrollTo(d.cursorRows(l.pageH))
	rows := d.rowLines(l.innerW, l.pageH)
	return renderDialogFrame(fmt.Sprintf("◇ worktrees · %d", len(d.worktrees)), rows, d.footer(), l), l
}

func (d *worktreeDialog) cursorRows(visible int) int {
	if d.cursor < len(d.worktrees)-1 && visible > 1 {
		return visible - 1
	}
	return visible
}

func maxWorktreeRowWidth(rows []string) int {
	width := 0
	for _, row := range rows {
		width = max(width, ansi.StringWidth(row))
	}
	return width
}

func (d *worktreeDialog) rowLines(inner, visible int) []string {
	visible = max(1, visible)
	if d.creating {
		return []string{tuiDimStyle.Render("creating worktree…")}
	}
	if len(d.worktrees) == 0 {
		return []string{tuiDimStyle.Render("no worktrees yet · c to create")}
	}
	var rows []string
	rowLimit := visible
	if d.scroll+rowLimit < len(d.worktrees) && rowLimit > 1 {
		rowLimit--
	}
	end := min(len(d.worktrees), d.scroll+rowLimit)
	for i := d.scroll; i < end; i++ {
		wt := d.worktrees[i]
		marker := "  "
		name := wt.Name
		if i == d.cursor {
			marker = tuiAccentStyle.Render("▸ ")
			name = lipgloss.NewStyle().Bold(true).Render(name)
		}
		meta := tuiDimStyle.Render(wt.Branch)
		line := marker + name
		gap := inner - lipgloss.Width(line) - lipgloss.Width(meta)
		if gap < 1 {
			line = truncateToWidth(line, max(8, inner-lipgloss.Width(meta)-1))
			gap = max(1, inner-lipgloss.Width(line)-lipgloss.Width(meta))
		}
		rows = append(rows, line+strings.Repeat(" ", gap)+meta)
	}
	return rows
}

func (d *worktreeDialog) footer() string {
	switch d.confirm {
	case wtConfirmDelete:
		if wt, ok := d.selected(); ok {
			return tuiErrorStyle.Render(fmt.Sprintf("delete %q? ", wt.Name)) +
				tuiDimStyle.Render("y confirm · n or esc cancel")
		}
	}
	if d.notice != "" {
		return tuiDimStyle.Render("↑↓ move · enter switch · b back to main · c create · d delete · esc close · ") + tuiInfoStyle.Render(d.notice)
	}
	return tuiDimStyle.Render("↑↓ move · enter switch · b back to main · c create · d delete · esc close")
}

// ─── Model wiring ─────────────────────────────────────────────────────

func (m *tuiModel) openWorktreeDialog() {
	m.closeSuggest()
	wtDir := m.resolveRepoRoot()
	list, err := vcs.List(context.Background(), wtDir)
	if err != nil {
		m.worktreeDlg = newWorktreeDialog(nil)
		m.worktreeDlg.notice = err.Error()
		m.hitMap.invalidate()
		return
	}
	m.worktreeDlg = newWorktreeDialog(list)
	m.hitMap.invalidate()
}

func (m *tuiModel) handleWorktreeDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.worktreeDlg
	visible := d.cursorRows(d.visibleRows(max(1, m.width), max(1, m.height)))
	if d.confirm != wtConfirmNone {
		switch key {
		case "y":
			m.applyWorktreeConfirm()
		case "n", "esc":
			d.confirm = wtConfirmNone
		}
		return true, true, nil
	}
	switch key {
	case "esc", "q":
		m.worktreeDlg = nil
		m.hitMap.invalidate()
	case "up", "k":
		d.move(-1)
		d.clampScrollTo(visible)
	case "down", "j":
		d.move(1)
		d.clampScrollTo(visible)
	case "enter":
		if wt, ok := d.selected(); ok {
			m.switchToWorktree(wt)
			return true, true, nil
		}
	case "d":
		if _, ok := d.selected(); ok {
			d.confirm = wtConfirmDelete
		}
	case "b":
		m.switchToMainTree()
		return true, true, nil
	case "c":
		if !d.creating {
			return true, true, []tea.Cmd{m.createWorktreeFromDialog()}
		}
	}
	return true, true, nil
}

func (m *tuiModel) applyWorktreeConfirm() {
	d := m.worktreeDlg
	switch d.confirm {
	case wtConfirmDelete:
		wt, ok := d.selected()
		if !ok {
			break
		}
		wtDir := m.resolveRepoRoot()
		if err := vcs.Remove(context.Background(), wtDir, wt.Name); err != nil {
			d.notice = "delete failed: " + err.Error()
			break
		}
		name := wt.Name
		d.removeAt(d.cursor)
		d.notice = fmt.Sprintf("deleted %q", name)
		m.refreshGitContext()
	}
	d.confirm = wtConfirmNone
}

// applyWorktreeCreated processes the async worktree creation result on the main
// goroutine (bubbletea Update), safely mutating dialog fields without a data race.
func (m *tuiModel) applyWorktreeCreated(msg worktreeCreatedMsg) {
	d := m.worktreeDlg
	if d == nil {
		return
	}
	d.creating = false
	if msg.err != nil {
		d.notice = "create failed: " + msg.err.Error()
		m.hitMap.invalidate()
		return
	}
	d.worktrees = append(d.worktrees, *msg.wt)
	d.cursor = len(d.worktrees) - 1
	d.notice = fmt.Sprintf("created %q at %s", msg.wt.Name, msg.wt.Path)
	d.clampScroll()
	m.refreshGitContext()
	m.hitMap.invalidate()
}

// worktreeCreatedMsg is delivered back to the bubbletea Update loop after
// the asynchronous vcs.Create call finishes, avoiding a data race on dialog
// fields that the View goroutine reads every frame.
type worktreeCreatedMsg struct {
	wt   *vcs.WorktreeInfo
	err  error
	desc string // human-readable name used for the notice
}

func (m *tuiModel) createWorktreeFromDialog() tea.Cmd {
	d := m.worktreeDlg
	if d == nil || d.creating {
		return nil
	}
	d.creating = true
	d.notice = ""
	m.hitMap.invalidate() // re-render to show "creating worktree…" placeholder
	wtDir := m.resolveRepoRoot()
	desc := fmt.Sprintf("wt-%d", len(d.worktrees)+1)
	return m.createWorktreeAsync(wtDir, desc)
}

// createWorktreeAsync runs vcs.Create in a goroutine and returns a command
// that delivers the result as a worktreeCreatedMsg back to the Update loop.
func (m *tuiModel) createWorktreeAsync(dir, desc string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		wt, err := vcs.Create(ctx, dir, desc, "")
		return worktreeCreatedMsg{wt: wt, err: err, desc: desc}
	}
}

// switchToWorktree changes the process working directory to the worktree,
// updates the model's cached workspace path and git context, then closes
// the dialog so the user lands back in chat with the new context.
func (m *tuiModel) switchToWorktree(wt vcs.WorktreeInfo) {
	if m.waiting {
		m.worktreeDlg.notice = "cannot switch while agent is running"
		return
	}
	if err := os.Chdir(wt.Path); err != nil {
		m.worktreeDlg.notice = "switch failed: " + err.Error()
		return
	}
	m.workspaceDir = shortenWorkspacePath()
	m.refreshGitContext()
	m.worktreeDlg = nil
	m.hitMap.invalidate()
}

// switchToMainTree changes back to the repository root (main tree).
func (m *tuiModel) switchToMainTree() {
	if m.waiting {
		m.worktreeDlg.notice = "cannot switch while agent is running"
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		dir = "."
	}
	root, err := vcs.MainRepoRoot(dir)
	if err != nil {
		m.worktreeDlg.notice = "not inside a git repo"
		return
	}
	if err := os.Chdir(root); err != nil {
		m.worktreeDlg.notice = "switch failed: " + err.Error()
		return
	}
	m.workspaceDir = shortenWorkspacePath()
	m.refreshGitContext()
	m.worktreeDlg = nil
	m.hitMap.invalidate()
}

// resolveWorkspaceDir returns the absolute workspace directory path.
func (m *tuiModel) resolveWorkspaceDir() string {
	dir := m.workspaceDir
	if dir != "" && strings.HasPrefix(dir, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = strings.Replace(dir, "~", home, 1)
		}
	}
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "."
		}
		return wd
	}
	return dir
}

// resolveRepoRoot returns the main repository root directory, which
// must be used (not the cwd) when creating/listing/removing worktrees.
// If the cwd is already the main root, this is a no-op. If resolution
// fails, it falls back to resolveWorkspaceDir().
func (m *tuiModel) resolveRepoRoot() string {
	dir := m.resolveWorkspaceDir()
	if root, err := vcs.MainRepoRoot(dir); err == nil {
		return root
	}
	return dir
}
