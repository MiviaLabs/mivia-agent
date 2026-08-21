package cli

import (
	"os"
	"path/filepath"
	"strings"
)

var (
	statWorktreeSwitchPath = os.Stat
	absWorktreeSwitchPath  = filepath.Abs
	evalWorktreeSwitchPath = filepath.EvalSymlinks
	getwdWorktreeSwitch    = os.Getwd
	relWorktreeSwitchPath  = filepath.Rel
)

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
