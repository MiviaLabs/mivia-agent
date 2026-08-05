package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

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
	canonicalPath, err := canonicalMarkerRoot(wt.Path)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	root := m.resolveRepoRoot()
	store, closeStore, err := m.worktreeLifecycleStore(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	defer closeStore()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	if err := store.ValidateActiveWorktreeInstance(context.Background(), principal, instance, canonicalPath); err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	return instance, nil
}

func (m *tuiModel) validateWorktreeWithoutMarker(wt vcs.WorktreeInfo) error {
	canonicalPath, err := canonicalMarkerRoot(wt.Path)
	if err != nil {
		return err
	}
	root := m.resolveRepoRoot()
	store, closeStore, err := m.worktreeLifecycleStore(root)
	if err != nil {
		return err
	}
	defer closeStore()
	principal, err := worktreeRoutePrincipal(root)
	if err != nil {
		return err
	}
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
