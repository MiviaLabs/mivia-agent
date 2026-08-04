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
		return tuiDimStyle.Render("c create · d delete · enter copy path · ") + tuiInfoStyle.Render(d.notice)
	}
	return tuiDimStyle.Render("↑↓ move · enter copy path · c create · d delete · esc close")
}

// ─── Model wiring ─────────────────────────────────────────────────────

func (m *tuiModel) openWorktreeDialog() {
	m.closeSuggest()
	wtDir := m.workspaceDir
	if wtDir != "" && strings.HasPrefix(wtDir, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			wtDir = strings.Replace(wtDir, "~", home, 1)
		}
	}
	// If workspaceDir is still relative or empty, use cwd.
	if wtDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			wd = "."
		}
		wtDir = wd
	}
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
			d.notice = wt.Path
		}
	case "d":
		if _, ok := d.selected(); ok {
			d.confirm = wtConfirmDelete
		}
	case "c":
		m.createWorktreeFromDialog()
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
		wtDir := m.resolveWorkspaceDir()
		if err := vcs.Remove(context.Background(), wtDir, wt.Name); err != nil {
			d.notice = "delete failed: " + err.Error()
			break
		}
		name := wt.Name
		d.removeAt(d.cursor)
		d.notice = fmt.Sprintf("deleted %q", name)
	}
	d.confirm = wtConfirmNone
}

func (m *tuiModel) createWorktreeFromDialog() {
	d := m.worktreeDlg
	if d == nil || d.creating {
		return
	}
	d.creating = true
	d.notice = ""
	wtDir := m.resolveWorkspaceDir()
	go func() {
		ctx := context.Background()
		// Use a generated name based on timestamp if no prompting is available.
		name := fmt.Sprintf("wt-%d", len(d.worktrees)+1)
		wt, err := vcs.Create(ctx, wtDir, name, "")
		if err != nil {
			d.notice = "create failed: " + err.Error()
			d.creating = false
			return
		}
		d.worktrees = append(d.worktrees, *wt)
		d.cursor = len(d.worktrees) - 1
		d.notice = fmt.Sprintf("created %q at %s", wt.Name, wt.Path)
		d.creating = false
		d.clampScroll()
	}()
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
