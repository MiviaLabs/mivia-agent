package cli

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

var (
	statWorktreeSwitchPath = os.Stat
	absWorktreeSwitchPath  = filepath.Abs
	evalWorktreeSwitchPath = filepath.EvalSymlinks
	getwdWorktreeSwitch    = os.Getwd
	relWorktreeSwitchPath  = filepath.Rel
)

func (m *tuiModel) switchToWorktree(wt vcs.WorktreeInfo) {
	if m.workspaceSwitchBusy() {
		m.worktreeDlg.setNotice("cannot switch while agent is running", true)
		return
	}
	instance, err := m.validateWorktreeSwitch(wt)
	if err != nil {
		m.worktreeDlg.setNotice("switch failed: "+err.Error(), true)
		return
	}
	if binding, ok := m.worktreeDlg.bindings[wt.Name]; ok {
		if binding.Err != nil || binding.Instance != instance {
			err := binding.Err
			if err == nil {
				err = contextstate.ErrWorktreeDeleted
			}
			m.worktreeDlg.setNotice("switch failed: "+err.Error(), true)
			return
		}
		instance = binding.Instance
	}
	if info, err := statWorktreeSwitchPath(wt.Path); err != nil {
		m.worktreeDlg.setNotice("switch failed: "+err.Error(), true)
		return
	} else if !info.IsDir() {
		m.worktreeDlg.setNotice("switch failed: path is not a directory", true)
		return
	}
	m.restartInWorkspace(wt.Path)
	m.restartWorktreeInstance = instance
}

func (m *tuiModel) switchToMainTree() {
	if m.workspaceSwitchBusy() {
		m.worktreeDlg.setNotice("cannot switch while agent is running", true)
		return
	}
	dir, _ := os.Getwd()
	root, err := vcs.MainRepoRoot(dir)
	if err != nil {
		m.worktreeDlg.setNotice("not inside a git repo", true)
		return
	}
	m.restartInWorkspace(root)
}

func (m *tuiModel) workspaceSwitchBusy() bool {
	return m.waiting || m.cancelling
}

func (m *tuiModel) restartInWorkspace(dir string) {
	abs, err := absWorktreeSwitchPath(dir)
	if err != nil {
		m.worktreeDlg.setNotice("switch failed: "+err.Error(), true)
		return
	}
	m.workspaceDir = abs
	m.restartWorkspace = abs
	m.restartWorktreeInstance = contextstate.WorktreeInstance{}
	m.worktreeDlg = nil
	m.hitMap.invalidate()
}

func worktreeContainsCurrentDir(path string) bool {
	root, err := absWorktreeSwitchPath(path)
	if err != nil {
		return true
	}
	if resolved, err := evalWorktreeSwitchPath(root); err == nil {
		root = resolved
	}
	cwd, err := getwdWorktreeSwitch()
	if err != nil {
		return true
	}
	if cwd, err = evalWorktreeSwitchPath(cwd); err != nil {
		return true
	}
	rel, err := relWorktreeSwitchPath(root, cwd)
	if err != nil {
		return true
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (m *tuiModel) resolveWorkspaceDir() string {
	dir := m.workspaceDir
	if dir == "~" || strings.HasPrefix(dir, "~"+string(filepath.Separator)) {
		if home, err := workspace.UserHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	if dir == "" {
		wd, _ := os.Getwd()
		return wd
	}
	return dir
}

func (m *tuiModel) resolveRepoRoot() string {
	dir := m.resolveWorkspaceDir()
	if root, err := vcs.MainRepoRoot(dir); err == nil {
		return root
	}
	return dir
}
