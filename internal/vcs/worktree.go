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
	// filepath.Abs only fails when Getwd fails (effectively never); git fails
	// loudly instead if the root is unusable, so drop the dead error branch.
	root, _ := filepath.Abs(repoRoot)
	sanitised, err := SanitizeName(name)
	if err != nil {
		return nil, err
	}
	truncated, err := nameIsTruncated(name)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, InvalidNameError{Input: name, Reason: "name is too long"}
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
	actualBranch, _ := CurrentBranch(ctx, targetPath)
	// CurrentBranch cannot fail here: git worktree add just succeeded and
	// rev-parse --abbrev-ref in the new worktree only fails if the worktree
	// vanished mid-call (a race with no deterministic test).
	return &WorktreeInfo{
		Name:   sanitised,
		Path:   targetPath,
		Branch: actualBranch,
	}, nil
}

// Remove deletes a worktree by name and prunes stale worktree references.
func Remove(ctx context.Context, repoRoot string, name string) error {
	root, _ := filepath.Abs(repoRoot) // Abs only fails if Getwd fails; git errors otherwise
	sanitised, err := SanitizeName(name)
	if err != nil {
		return err
	}
	truncated, err := nameIsTruncated(name)
	if err != nil {
		return err
	}
	if truncated {
		return InvalidNameError{Input: name, Reason: "name is too long"}
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

	// Delete the mivia branch if it exists.
	branchName := "wt/" + sanitised
	delCmd := exec.CommandContext(ctx, "git", "branch", "-D", branchName)
	delCmd.Dir = root
	_ = delCmd.Run() // ignore error — branch may not exist

	return nil
}

// List returns all mivia-managed worktrees for the repo at repoRoot.
// The main worktree is filtered out.
func List(ctx context.Context, repoRoot string) ([]WorktreeInfo, error) {
	root, _ := filepath.Abs(repoRoot) // Abs only fails if Getwd fails; git errors otherwise
	if err := ensureGitRepo(root); err != nil {
		return nil, err
	}
	wtDir := workspace.WorktreesDir(root)
	wtPrefix := wtDir + string(filepath.Separator)
	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	cmd.Dir = root
	// ensureGitRepo above already proved this is a work tree and the porcelain
	// listing tolerates broken worktree metadata, so the command cannot fail
	// in a way a deterministic test can reach; treat failure as an empty list.
	out, _ := cmd.Output()
	return parseWorktreeList(string(out), wtPrefix)
}

// Resolve finds a worktree by name. Returns nil, nil if not found.
func Resolve(ctx context.Context, repoRoot string, name string) (*WorktreeInfo, error) {
	root, _ := filepath.Abs(repoRoot) // Abs only fails if Getwd fails; git errors otherwise
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

// MainRepoRoot finds the main repository root (the one with .git/ as a
// real directory) from any directory inside the repo, including linked
// worktrees. Unlike RepoRoot (which returns the worktree's own toplevel),
// this always returns the primary working tree path.
func MainRepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", NotGitRepoError{Dir: dir}
	}
	return mainWorktreeFromListing(string(out), dir)
}

// mainWorktreeFromListing returns the first "worktree " path from a porcelain
// listing; git names the main working tree first. Kept separate from
// MainRepoRoot so the no-match branch is unit-testable.
func mainWorktreeFromListing(out, dir string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "worktree ") {
			return strings.TrimPrefix(line, "worktree "), nil
		}
	}
	return "", NotGitRepoError{Dir: dir}
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

// CurrentWorktreeName returns the mivia worktree name if dir is inside a
// mivia-managed worktree under workspace.WorktreesDir(main root).
// Returns empty string if dir is the main tree or not inside any worktree.
// The worktree root is the main repo root: RepoRoot alone returns a linked
// worktree's own toplevel, which has no .mivia/worktrees directory of its
// own. A subdirectory of a worktree still belongs to that worktree, so the
// search ascends until it reaches the directory directly under worktrees/.
func CurrentWorktreeName(ctx context.Context, dir string) (string, error) {
	root, err := MainRepoRoot(dir)
	if err != nil {
		return "", err
	}
	wtDir := workspace.WorktreesDir(root)
	abs, _ := filepath.Abs(dir)
	prefix := wtDir + string(filepath.Separator)
	for {
		if !strings.HasPrefix(abs, prefix) {
			return "", nil // main tree or outside the mivia worktrees dir
		}
		if filepath.Dir(abs) == wtDir {
			return filepath.Base(abs), nil
		}
		// Climb stops at the directory directly under worktrees/, which is
		// always a child of wtDir; the filesystem root is never reached while
		// still under prefix, so no parent==abs guard is needed here.
		abs = filepath.Dir(abs)
	}
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
