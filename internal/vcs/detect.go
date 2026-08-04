package vcs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// DetectBranch returns the current HEAD branch name, or empty if not a repo.
func DetectBranch() string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir, _ = os.Getwd()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DetectWorktreeName returns the worktree name if the cwd is inside a git
// worktree. For mivia-managed worktrees it returns the directory name under
// .mivia/worktrees/. For other linked worktrees it returns the base directory
// name. Returns empty for the main working tree.
func DetectWorktreeName() string {
	dir, _ := os.Getwd()
	root, err := RepoRoot(dir)
	if err != nil {
		return ""
	}
	wtDir := workspace.WorktreesDir(root)
	abs, _ := filepath.Abs(dir)
	if strings.HasPrefix(abs, wtDir+string(filepath.Separator)) {
		return filepath.Base(abs)
	}
	// Fallback: detect any non-mivia git worktree.
	if IsWorktree() {
		return filepath.Base(abs)
	}
	return ""
}

// IsWorktree returns true if the current directory is inside a git worktree
// (as opposed to the main working tree).
func IsWorktree() bool {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir, _ = os.Getwd()
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	commonDir := strings.TrimSpace(string(out))
	if commonDir == "" {
		return false
	}
	// Resolve to absolute path.
	abs, _ := filepath.Abs(commonDir)
	// If common-dir differs from the repo's .git dir, we're in a worktree.
	dir, _ := os.Getwd()
	root, err := RepoRoot(dir)
	if err != nil {
		return false
	}
	repoGitDir := filepath.Join(root, ".git")
	repoGitDir = resolveGitDir(repoGitDir)
	return abs != repoGitDir
}

// resolveGitDir follows a .git file's gitdir: pointer to the actual git
// directory. Returns the path unchanged if it is a directory or the file
// cannot be read.
func resolveGitDir(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return path
	}
	if !info.Mode().IsRegular() {
		return path // it's a directory, not a file
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return path
	}
	line := strings.TrimSpace(string(data))
	if strings.HasPrefix(line, "gitdir: ") {
		resolved, _ := filepath.Abs(strings.TrimPrefix(line, "gitdir: "))
		return resolved
	}
	return path
}
