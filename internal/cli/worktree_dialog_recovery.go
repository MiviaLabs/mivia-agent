package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

var canonicalWorktreeDialogRoot = canonicalMarkerRoot

type worktreeRecoveryRow struct {
	Info contextstate.WorktreeInstanceInfo
}

type worktreeDialogBinding struct {
	Instance contextstate.WorktreeInstance
	Err      error
}

func (d *worktreeDialog) setRecovery(info contextstate.WorktreeInstanceInfo) {
	d.recovery[info.Instance.Worktree] = worktreeRecoveryRow{Info: info}
}

func (d *worktreeDialog) selectedRecovery() (worktreeRecoveryRow, bool) {
	worktree, ok := d.selected()
	if !ok {
		return worktreeRecoveryRow{}, false
	}
	recovery, ok := d.recovery[worktree.Name]
	return recovery, ok
}

func worktreeRecoveryLabel(state contextstate.WorktreeInstanceState) string {
	if state == contextstate.WorktreeCreating {
		return "creation recovery required"
	}
	return "recovery required"
}

func (m *tuiModel) validateWorktreeSwitch(wt vcs.WorktreeInfo) (contextstate.WorktreeInstance, error) {
	instance, err := readWorktreeMarker(wt.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return contextstate.WorktreeInstance{}, m.validateWorktreeWithoutMarker(wt)
		}
		return contextstate.WorktreeInstance{}, err
	}
	if instance.Worktree != wt.Name {
		return contextstate.WorktreeInstance{}, contextstate.ErrWorktreeDeleted
	}
	canonicalPath, err := canonicalWorktreeDialogRoot(wt.Path)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	root := m.resolveRepoRoot()
	store, closeStore, err := m.worktreeLifecycleStore(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	defer closeStore()
	principal, _ := worktreeRoutePrincipal(root)
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, canonicalPath); err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	return instance, nil
}

func (m *tuiModel) validateWorktreeWithoutMarker(wt vcs.WorktreeInfo) error {
	canonicalPath, err := canonicalWorktreeDialogRoot(wt.Path)
	if err != nil {
		return err
	}
	root := m.resolveRepoRoot()
	store, closeStore, err := m.worktreeLifecycleStore(root)
	if err != nil {
		return err
	}
	defer closeStore()
	principal, _ := worktreeRoutePrincipal(root)
	info, legacy, err := classifyMissingWorktreeMarker(store, principal, wt.Name, canonicalPath)
	if err != nil {
		return err
	}
	if !info.Instance.IsZero() {
		return fmt.Errorf("managed worktree %q has state %q but no marker: %w", wt.Name, info.State, contextstate.ErrWorktreeDeleted)
	}
	if legacy {
		return fmt.Errorf("worktree %q requires adoption; run mivia worktree adopt %s", wt.Name, wt.Name)
	}
	return nil
}

func (m *tuiModel) validateSessionWorktree(dir string, expected contextstate.WorktreeInstance) error {
	if expected.IsZero() {
		return nil
	}
	root := m.resolveRepoRoot()
	store, closeStore, err := m.worktreeLifecycleStore(root)
	if err != nil {
		return err
	}
	defer closeStore()
	return validateExpectedWorktreeInstanceInStore(store, root, dir, expected)
}

func (m *tuiModel) recoverCreatingWorktree(wt vcs.WorktreeInfo, info contextstate.WorktreeInstanceInfo) {
	if m.workspaceSwitchBusy() {
		m.worktreeDlg.setNotice("cannot switch while agent is running", true)
		return
	}
	root := m.resolveRepoRoot()
	store, closeStore, err := m.worktreeLifecycleStore(root)
	if err != nil {
		m.worktreeDlg.setNotice("creation recovery failed: "+err.Error(), true)
		return
	}
	handled, err := m.abandonStaleWorktreeCreation(store, root, wt, info)
	if err != nil {
		closeStore()
		m.worktreeDlg.setNotice("creation recovery failed: "+err.Error(), true)
		return
	}
	if handled {
		closeStore()
		return
	}
	recovered, err := recoverManagedWorktreeCreationInStore(store, root, info)
	closeStore()
	if err != nil {
		m.worktreeDlg.setNotice("creation recovery failed: "+err.Error(), true)
		return
	}
	if recovered.Name != wt.Name || recovered.Path != wt.Path {
		m.worktreeDlg.setNotice("creation recovery returned a different worktree", true)
		return
	}
	m.restartInWorkspace(recovered.Path)
	m.restartWorktreeInstance = info.Instance
}

// abandonStaleWorktreeCreation removes a creating instance whose Git worktree
// never materialized. It reports handled=true when it removed the row or hit
// an error (err non-nil). It reports handled=false when the expected worktree
// path exists and the caller must continue with normal recovery or deletion.
func (m *tuiModel) abandonStaleWorktreeCreation(store *storage.SQLite, root string, wt vcs.WorktreeInfo, info contextstate.WorktreeInstanceInfo) (bool, error) {
	if _, err := os.Stat(info.CanonicalPath); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return true, fmt.Errorf("inspect expected worktree path: %w", err)
	}
	principal, _ := worktreeRoutePrincipal(root)
	if err := store.AbandonWorktreeCreation(context.Background(), principal, info.Instance); err != nil {
		return true, err
	}
	name := wt.Name
	m.worktreeDlg.removeAt(m.worktreeDlg.cursor)
	m.worktreeDlg.setNotice(fmt.Sprintf("abandoned incomplete creation of %q", name), false)
	m.refreshGitContext()
	return true, nil
}
