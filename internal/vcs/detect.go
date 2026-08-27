package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DetectBranch returns the current HEAD branch name, or empty if not a repo.
func DetectBranch() string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir, _ = os.Getwd()
	cmd.Env = pinnedEnv()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DetectWorktreeName returns the worktree name if the cwd is inside a git
// worktree. For mivia-managed worktrees it returns the directory name under
// .mivia/worktrees/ (resolving subdirectories to the worktree root). For
// other linked worktrees it returns the base directory name. Returns empty
// for the main working tree.
func DetectWorktreeName() string {
	dir, _ := os.Getwd()
	if name, err := CurrentWorktreeName(context.Background(), dir); err == nil && name != "" {
		return name
	}
	// Fallback: detect any non-mivia git worktree.
	if IsWorktree() {
		abs, _ := filepath.Abs(dir)
		return filepath.Base(abs)
	}
	return ""
}

// IsWorktree returns true if the current directory is inside a git worktree
// (as opposed to the main working tree).
func IsWorktree() bool {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir, _ = os.Getwd()
	cmd.Env = pinnedEnv()
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	commonDir := strings.TrimSpace(string(out))
	// On success git always prints the common dir; the error branch above
	// already covers git absence, so an empty string is unreachable.
	// Resolve to absolute path.
	abs, _ := filepath.Abs(commonDir)
	// If common-dir differs from the repo's .git dir, we're in a worktree.
	dir, _ := os.Getwd()
	// rev-parse --git-common-dir succeeded above, so the repo is valid and
	// RepoRoot cannot fail here; drop its error to keep the dead branch out
	// of the coverage surface.
	root, _ := RepoRoot(dir)
	repoGitDir := filepath.Join(root, ".git")
	repoGitDir = resolveGitDir(repoGitDir)
	return abs != repoGitDir
}

// ResolveGitDir follows a .git file's gitdir: pointer to the actual git
// directory. Returns the path unchanged if it is a directory, a plain
// file that is not a valid gitdir pointer, or cannot be read. It performs
// no process execution, so callers outside this package (for example a
// no-exec workspace/branch detector) can reuse the same resolution logic
// this package already uses for worktree detection instead of
// reimplementing gitdir-pointer parsing.
func ResolveGitDir(path string) string {
	return resolveGitDir(path)
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
	// A read failure or a non-gitdir body both mean the file is not a valid
	// gitdir pointer, so they share one fallback.
	if err != nil {
		return path
	}
	pointer, ok := gitdirPointer(data)
	if !ok {
		return path
	}
	// Git's read_gitfile_gently anchors a relative pointer to the directory
	// that contains the .git file, not to the process working directory.
	if !filepath.IsAbs(pointer) {
		pointer = filepath.Join(filepath.Dir(path), pointer)
	}
	resolved, _ := filepath.Abs(pointer)
	return resolved
}

// gitdirPointer extracts the git directory pointer from a .git gitfile.
// It returns the pointer and true when the trimmed content starts with the
// 'gitdir: ' prefix and the first line after the prefix is non-empty. The
// pointer is the first line, trimmed; later lines are ignored. Any other
// content yields ("", false).
func gitdirPointer(data []byte) (string, bool) {
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "gitdir: ") {
		return "", false
	}
	rest := strings.TrimPrefix(trimmed, "gitdir: ")
	line, _, _ := strings.Cut(rest, "\n")
	pointer := strings.TrimSpace(line)
	if pointer == "" {
		return "", false
	}
	return pointer, true
}
