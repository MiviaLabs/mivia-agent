package legacytui

import (
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func (m *TUIModel) switchToWorktree(wt vcs.WorktreeInfo) {
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
	if info, err := cli.StatWorktreeSwitchPath(wt.Path); err != nil {
		m.worktreeDlg.setNotice("switch failed: "+err.Error(), true)
		return
	} else if !info.IsDir() {
		m.worktreeDlg.setNotice("switch failed: path is not a directory", true)
		return
	}
	m.restartInWorkspace(wt.Path)
	m.restartWorktreeInstance = instance
}

func (m *TUIModel) switchToMainTree() {
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

func (m *TUIModel) workspaceSwitchBusy() bool {
	return m.waiting || m.cancelling
}

func (m *TUIModel) restartInWorkspace(dir string) {
	if !filepath.IsAbs(dir) {
		cwd, err := cli.GetwdWorktreeSwitch()
		if err != nil {
			m.worktreeDlg.setNotice("switch failed: "+err.Error(), true)
			return
		}
		if _, err := cli.StatWorktreeSwitchPath(cwd); err != nil {
			m.worktreeDlg.setNotice("switch failed: "+err.Error(), true)
			return
		}
	}
	abs, err := cli.AbsWorktreeSwitchPath(dir)
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

func (m *TUIModel) resolveWorkspaceDir() string {
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

func (m *TUIModel) resolveRepoRoot() string {
	dir := m.resolveWorkspaceDir()
	if root, err := vcs.MainRepoRoot(dir); err == nil {
		return root
	}
	return dir
}
