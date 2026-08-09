package cli

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
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
	worktrees            []vcs.WorktreeInfo
	recovery             map[string]worktreeRecoveryRow
	bindings             map[string]worktreeDialogBinding
	cursor               int
	scroll               int
	confirm              worktreeConfirm
	notice               string
	noticeErr            bool
	creating             bool
	lifecycleUnavailable bool
}

func newWorktreeDialog(worktrees []vcs.WorktreeInfo) *worktreeDialog {
	return &worktreeDialog{worktrees: append([]vcs.WorktreeInfo(nil), worktrees...), recovery: make(map[string]worktreeRecoveryRow), bindings: make(map[string]worktreeDialogBinding)}
}

func (d *worktreeDialog) setNotice(msg string, isErr bool) {
	d.notice = msg
	d.noticeErr = isErr
}

func oneLineNotice(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

func newWorktreeName() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "wt-" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}

func (d *worktreeDialog) removeAt(i int) {
	if i < 0 || i >= len(d.worktrees) {
		return
	}
	delete(d.recovery, d.worktrees[i].Name)
	delete(d.bindings, d.worktrees[i].Name)
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
		metaText := wt.Branch
		if recovery, ok := d.recovery[wt.Name]; ok {
			metaText = worktreeRecoveryLabel(recovery.Info.State)
		}
		meta := tuiDimStyle.Render(metaText)
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
		style := tuiInfoStyle
		if d.noticeErr {
			style = tuiErrorStyle
		}
		return tuiDimStyle.Render("↑↓ move · enter switch · b back to main · c create · d delete · esc close · ") + style.Render(oneLineNotice(d.notice))
	}
	return tuiDimStyle.Render("↑↓ move · enter switch · b back to main · c create · d delete · esc close")
}

func (m *tuiModel) handleWorktreeDialogKey(key string) (bool, bool, []tea.Cmd) {
	d := m.worktreeDlg
	visible := d.cursorRows(d.visibleRows(max(1, m.width), max(1, m.height)))
	if d.creating {
		return true, true, nil
	}
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
		if d.lifecycleUnavailable {
			d.setNotice("worktree lifecycle state is unavailable", true)
			return true, true, nil
		}
		if wt, ok := d.selected(); ok {
			if recovery, found := d.selectedRecovery(); found && recovery.Info.State == contextstate.WorktreeDeleting {
				d.setNotice("remove this row to recover deletion", true)
				return true, true, nil
			}
			if recovery, found := d.selectedRecovery(); found && recovery.Info.State == contextstate.WorktreeCreating {
				m.recoverCreatingWorktree(wt, recovery.Info)
				return true, true, nil
			}
			m.switchToWorktree(wt)
			return true, true, nil
		}
	case "d":
		if d.lifecycleUnavailable {
			d.setNotice("worktree lifecycle state is unavailable", true)
			return true, true, nil
		}
		if _, ok := d.selected(); ok {
			d.confirm = wtConfirmDelete
		}
	case "b":
		m.switchToMainTree()
		return true, true, nil
	case "c":
		if d.lifecycleUnavailable {
			d.setNotice("worktree lifecycle state is unavailable", true)
			return true, true, nil
		}
		if m.workspaceSwitchBusy() {
			d.setNotice("cannot switch while agent is running", true)
			return true, true, nil
		}
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
		m.applyWorktreeDeleteConfirm(d)
	}
	d.confirm = wtConfirmNone
}

// applyWorktreeDeleteConfirm deletes the selected worktree row. The caller
// clears the confirm state after it returns.
func (m *tuiModel) applyWorktreeDeleteConfirm(d *worktreeDialog) {
	wt, ok := d.selected()
	if !ok {
		return
	}
	wtDir := m.resolveRepoRoot()
	worktreeConfig, err := config.LoadWorktreeConfig(wtDir)
	if err != nil {
		d.setNotice("delete failed: "+err.Error(), true)
		return
	}
	if m.handleWorktreeDeleteRecovery(wtDir, wt, worktreeConfig.BranchPrefix) {
		return
	}
	if worktreeContainsCurrentDir(wt.Path) {
		d.setNotice("cannot delete the current worktree", true)
		return
	}
	lock, lockErr := lockWorktreeLifecycle(wtDir, wt.Name)
	if lockErr != nil {
		d.setNotice("delete failed: "+lockErr.Error(), true)
		return
	}
	defer lock.Close()
	expected, hasBinding := d.bindings[wt.Name]
	if hasBinding && !expected.Instance.IsZero() {
		// Only a concrete binding protects against same-name replacement.
		// An empty or broken binding (zero instance) means the worktree is
		// unmanaged, so deletion falls back to the unmanaged path below.
		current, validateErr := m.validateWorktreeSwitch(wt)
		if validateErr != nil || expected.Err != nil || current != expected.Instance {
			err := validateErr
			if err == nil {
				err = expected.Err
			}
			if err == nil {
				err = contextstate.ErrWorktreeDeleted
			}
			d.setNotice("delete failed: "+err.Error(), true)
			return
		}
	}
	requireExpected := hasBinding && !expected.Instance.IsZero()
	instance, err := beginManagedWorktreeRemovalForSessionExpected(m.session, wtDir, &wt, expected.Instance, requireExpected)
	if errors.Is(err, errUnmanagedWorktree) {
		// The worktree has no valid lifecycle binding (missing marker or no
		// storage entry). Remove it directly so its HDD space is freed.
		if err := removeUnmanagedWorktree(wtDir, &wt, worktreeConfig.BranchPrefix, lock.File()); err != nil {
			d.setNotice("delete failed: "+err.Error(), true)
		} else {
			name := wt.Name
			d.removeAt(d.cursor)
			d.setNotice(fmt.Sprintf("deleted %q", name), false)
			m.refreshGitContext()
		}
		return
	}
	if err != nil {
		d.setNotice("delete failed: "+err.Error(), true)
		return
	}
	if err := vcs.RemoveWithPrefixLease(context.Background(), wtDir, wt.Name, worktreeConfig.BranchPrefix, lock.File()); err != nil {
		if reactivateErr := reactivateManagedWorktreeForSession(m.session, wtDir, instance); reactivateErr != nil {
			d.setNotice("delete failed: "+err.Error()+"; session lifecycle recovery failed: "+reactivateErr.Error(), true)
		} else {
			d.setNotice("delete failed: "+err.Error(), true)
		}
		return
	}
	if err := finishManagedWorktreeRemovalForSession(m.session, wtDir, instance); err != nil {
		d.setNotice("deleted worktree but session cleanup failed: "+err.Error(), true)
		return
	}
	name := wt.Name
	d.removeAt(d.cursor)
	d.setNotice(fmt.Sprintf("deleted %q", name), false)
	m.refreshGitContext()
}

// handleWorktreeDeleteRecovery resolves a recovery row during delete confirm.
// It returns true when it handled the row and the caller must stop.
func (m *tuiModel) handleWorktreeDeleteRecovery(wtDir string, wt vcs.WorktreeInfo, branchPrefix string) bool {
	recovery, found := m.worktreeDlg.selectedRecovery()
	if !found {
		return false
	}
	if recovery.Info.State == contextstate.WorktreeCreating {
		return m.abandonStaleWorktreeCreationInDialog(wtDir, wt, recovery.Info)
	}
	if recovery.Info.State != contextstate.WorktreeDeleting {
		return false
	}
	store, closeStore, storeErr := m.worktreeLifecycleStore(wtDir)
	if storeErr != nil {
		m.worktreeDlg.setNotice("recovery failed: "+storeErr.Error(), true)
		return true
	}
	err := recoverManagedWorktreeRemovalInfoInStore(store, wtDir, recovery.Info, branchPrefix)
	closeStore()
	if err != nil {
		m.worktreeDlg.setNotice("recovery failed: "+fmt.Sprint(err), true)
		return true
	}
	m.openWorktreeDialog()
	m.worktreeDlg.setNotice(fmt.Sprintf("recovered removal of %q", wt.Name), false)
	return true
}

// abandonStaleWorktreeCreationInDialog removes a creating instance whose Git
// worktree never materialized. It returns true when it handled the row.
func (m *tuiModel) abandonStaleWorktreeCreationInDialog(wtDir string, wt vcs.WorktreeInfo, info contextstate.WorktreeInstanceInfo) bool {
	store, closeStore, storeErr := m.worktreeLifecycleStore(wtDir)
	if storeErr != nil {
		m.worktreeDlg.setNotice("delete failed: "+storeErr.Error(), true)
		return true
	}
	handled, abandonErr := m.abandonStaleWorktreeCreation(store, wtDir, wt, info)
	closeStore()
	if abandonErr != nil {
		m.worktreeDlg.setNotice("delete failed: "+abandonErr.Error(), true)
		return true
	}
	return handled
}

func (m *tuiModel) applyWorktreeCreated(msg worktreeCreatedMsg) {
	d := m.worktreeDlg
	if d == nil || msg.dlg != d {
		// The dialog that issued the create was closed (or replaced) before
		// the result arrived. The worktree exists on disk, but it must not be
		// appended to a dialog that did not request it, and the current
		// dialog's creating flag must not be cleared by a stale message.
		return
	}
	d.creating = false
	if msg.err != nil {
		if msg.wt != nil {
			d.worktrees = append(d.worktrees, *msg.wt)
			d.cursor = len(d.worktrees) - 1
			d.clampScroll()
		}
		d.setNotice("create failed: "+msg.err.Error(), true)
		m.hitMap.invalidate()
		return
	}
	if msg.wt == nil || msg.instance.IsZero() || msg.instance.Validate() != nil || msg.instance.Worktree != msg.wt.Name {
		d.setNotice("create failed: invalid worktree instance", true)
		m.hitMap.invalidate()
		return
	}
	d.worktrees = append(d.worktrees, *msg.wt)
	d.bindings[msg.wt.Name] = worktreeDialogBinding{Instance: msg.instance}
	d.cursor = len(d.worktrees) - 1
	d.setNotice(fmt.Sprintf("created %q at %s", msg.wt.Name, msg.wt.Path), false)
	d.clampScroll()
	m.refreshGitContext()
	m.hitMap.invalidate()
	if info, err := os.Stat(msg.wt.Path); err == nil && info.IsDir() {
		m.restartInWorkspace(msg.wt.Path)
		m.restartWorktreeInstance = msg.instance
	}
}

type worktreeCreatedMsg struct {
	wt       *vcs.WorktreeInfo
	instance contextstate.WorktreeInstance
	err      error
	dlg      *worktreeDialog
}

func (m *tuiModel) createWorktreeFromDialog() tea.Cmd {
	d := m.worktreeDlg
	if d == nil || d.creating || m.workspaceSwitchBusy() {
		return nil
	}
	d.creating = true
	d.setNotice("", false)
	m.hitMap.invalidate() // re-render to show "creating worktree…" placeholder
	wtDir := m.resolveRepoRoot()
	worktreeConfig, err := config.LoadWorktreeConfig(wtDir)
	if err != nil {
		d.creating = false
		d.setNotice("create failed: "+err.Error(), true)
		m.hitMap.invalidate()
		return nil
	}
	return m.createWorktreeAsyncWithPrefix(wtDir, newWorktreeName(), worktreeConfig.BranchPrefix, d)
}

func (m *tuiModel) createWorktreeAsync(dir, name string, dlg *worktreeDialog) tea.Cmd {
	repoRoot, err := vcs.MainRepoRoot(dir)
	if err != nil {
		return func() tea.Msg {
			return worktreeCreatedMsg{err: err, dlg: dlg}
		}
	}
	worktreeConfig, err := config.LoadWorktreeConfig(repoRoot)
	if err != nil {
		return func() tea.Msg {
			return worktreeCreatedMsg{err: err, dlg: dlg}
		}
	}
	return m.createWorktreeAsyncWithPrefix(repoRoot, name, worktreeConfig.BranchPrefix, dlg)
}
