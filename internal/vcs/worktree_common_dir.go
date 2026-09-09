package vcs

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// worktreeCommonDirWaitDelay bounds the git child after its context ends.
const worktreeCommonDirWaitDelay = 2 * time.Second

// WorktreeGitCommonDir resolves the repository's shared .git directory - the
// same path for the main checkout and every linked worktree, which is what
// makes it the right place to anchor a cross-process worktree lifecycle lock.
func WorktreeGitCommonDir(root string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.WaitDelay = worktreeCommonDirWaitDelay
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Git common directory: %w", err)
	}
	commonDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	commonDir, err = filepath.EvalSymlinks(filepath.Clean(commonDir))
	if err != nil {
		return "", fmt.Errorf("resolve Git common directory: %w", err)
	}
	return commonDir, nil
}
