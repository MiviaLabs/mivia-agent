package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// WorktreeInfo describes a single mivia-managed worktree.
type WorktreeInfo struct {
	Name   string // human name (sanitised)
	Path   string // absolute path on disk (under workspace.WorktreesDir())
	Branch string // checked-out branch/commit
}

// Create adds a new worktree under workspace.WorktreesDir(repoRoot).
// baseRef is the branch, tag, or SHA to check out.
// Returns the WorktreeInfo for the new worktree.
func Create(ctx context.Context, repoRoot string, name string, baseRef string) (*WorktreeInfo, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	sanitised, err := SanitizeName(name)
	if err != nil {
		return nil, err
	}
	if err := ensureGitRepo(root); err != nil {
		return nil, err
	}
	wtDir := workspace.WorktreesDir(root)
	targetPath := filepath.Join(wtDir, sanitised)
	if _, err := os.Stat(targetPath); err == nil {
		return nil, WorktreeExistsError{Name: sanitised}
	}
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		return nil, err
	}
	// git worktree add <path> -b <branch> <baseRef>
	// If baseRef is empty, use HEAD.
	ref := baseRef
	if ref == "" {
		ref = "HEAD"
	}
	branchName := "wt/" + sanitised
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", targetPath, "-b", branchName, ref)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, &gitCommandError{cmd: "worktree add", output: string(out), err: err}
	}
	actualBranch, err := CurrentBranch(ctx, targetPath)
	if err != nil {
		actualBranch = ref
	}
	return &WorktreeInfo{
		Name:   sanitised,
		Path:   targetPath,
		Branch: actualBranch,
	}, nil
}

// Remove deletes a worktree by name and prunes stale worktree references.
func Remove(ctx context.Context, repoRoot string, name string) error {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return err
	}
	sanitised, err := SanitizeName(name)
	if err != nil {
		return err
	}
	if err := ensureGitRepo(root); err != nil {
		return err
	}
	wtDir := workspace.WorktreesDir(root)
	targetPath := filepath.Join(wtDir, sanitised)
	if _, err := os.Stat(targetPath); err != nil {
		return WorktreeNotFoundError{Name: sanitised}
	}
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", targetPath, "--force")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return &gitCommandError{cmd: "worktree remove", output: string(out), err: err}
	}
	// Prune stale references.
	prune := exec.CommandContext(ctx, "git", "worktree", "prune")
	prune.Dir = root
	_ = prune.Run()
	return nil
}

// List returns all mivia-managed worktrees for the repo at repoRoot.
// The main worktree is filtered out.
func List(ctx context.Context, repoRoot string) ([]WorktreeInfo, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	if err := ensureGitRepo(root); err != nil {
		return nil, err
	}
	wtDir := workspace.WorktreesDir(root)
	wtPrefix := wtDir + string(filepath.Separator)
	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, &gitCommandError{cmd: "worktree list", output: string(out), err: err}
	}
	return parseWorktreeList(string(out), wtPrefix)
}

// Resolve finds a worktree by name. Returns nil, nil if not found.
func Resolve(ctx context.Context, repoRoot string, name string) (*WorktreeInfo, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	sanitised, err := SanitizeName(name)
	if err != nil {
		return nil, err
	}
	list, err := List(ctx, root)
	if err != nil {
		return nil, err
	}
	for _, wt := range list {
		if wt.Name == sanitised {
			return &wt, nil
		}
	}
	return nil, nil
}

// RepoRoot finds the git repository root from any directory inside it.
func RepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", NotGitRepoError{Dir: dir}
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentWorktreeName returns the worktree name if cwd is inside a
// mivia-managed worktree under workspace.WorktreesDir(repoRoot).
// Returns empty string if cwd is the main tree or not inside any worktree.
func CurrentWorktreeName(ctx context.Context, dir string) (string, error) {
	root, err := RepoRoot(dir)
	if err != nil {
		return "", err
	}
	wtDir := workspace.WorktreesDir(root)
	abs, _ := filepath.Abs(dir)
	if !strings.HasPrefix(abs, wtDir+string(filepath.Separator)) {
		return "", nil // main tree
	}
	return filepath.Base(abs), nil
}

// --- name sanitisation is in naming.go ---
// --- error types are in errors.go ---

// --- internal helpers ---

type gitCommandError struct {
	cmd    string
	output string
	err    error
}

func (e *gitCommandError) Error() string {
	msg := "git " + e.cmd + ": " + e.err.Error()
	if e.output != "" {
		msg += ": " + strings.TrimSpace(e.output)
	}
	return msg
}

func ensureGitRepo(dir string) error {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return NotGitRepoError{Dir: dir}
	}
	return nil
}

// CurrentBranch returns the currently checked-out branch name for a worktree.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func parseWorktreeList(output, wtPrefix string) ([]WorktreeInfo, error) {
	var result []WorktreeInfo
	var wt struct {
		path   string
		branch string
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			// End of a worktree block.
			if wt.path != "" && strings.HasPrefix(wt.path, wtPrefix) {
				name := filepath.Base(wt.path)
				absPath, _ := filepath.Abs(wt.path)
				result = append(result, WorktreeInfo{
					Name:   name,
					Path:   absPath,
					Branch: wt.branch,
				})
			}
			wt = struct {
				path   string
				branch string
			}{}
			continue
		}
		if key, val, ok := strings.Cut(line, " "); ok {
			switch key {
			case "worktree":
				wt.path = val
			case "branch":
				wt.branch = strings.TrimPrefix(val, "refs/heads/")
			case "HEAD":
				// Detached HEAD — branch field stays empty
			}
		}
	}
	return result, nil
}
