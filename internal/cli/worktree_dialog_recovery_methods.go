package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/storage"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// *tuiModel-receiver methods for worktree switch/recovery validation. See
// worktree_dialog_recovery_dialog.go for the worktreeDialog-receiver
// recovery-row bookkeeping these methods drive.

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
	// A path whose parent is a regular file can never materialize, even
	// though Windows reports it as ERROR_PATH_NOT_FOUND (mapped to
	// IsNotExist) instead of "not a directory". Only the parent stat tells
	// the two apart, so treat that as an inspect error rather than silently
	// abandoning a creation that may still be in flight.
	if parent := filepath.Dir(info.CanonicalPath); parent != info.CanonicalPath {
		if st, statErr := os.Stat(parent); statErr == nil && !st.IsDir() {
			return true, fmt.Errorf("inspect expected worktree path: parent is not a directory")
		}
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
