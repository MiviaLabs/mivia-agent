package cliworktree

import (
	"os"
	"path/filepath"
	"strings"
)

var (
	StatWorktreeSwitchPath = os.Stat
	AbsWorktreeSwitchPath  = filepath.Abs
	EvalWorktreeSwitchPath = filepath.EvalSymlinks
	GetwdWorktreeSwitch    = os.Getwd
	RelWorktreeSwitchPath  = filepath.Rel
)

// WorktreeContainsCurrentDir implements worktree contains current dir.
func WorktreeContainsCurrentDir(path string) bool {
	root, err := AbsWorktreeSwitchPath(path)
	if err != nil {
		return true
	}
	if resolved, err := EvalWorktreeSwitchPath(root); err == nil {
		root = resolved
	}
	cwd, err := GetwdWorktreeSwitch()
	if err != nil {
		return true
	}
	if cwd, err = EvalWorktreeSwitchPath(cwd); err != nil {
		return true
	}
	rel, err := RelWorktreeSwitchPath(root, cwd)
	if err != nil {
		return true
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
