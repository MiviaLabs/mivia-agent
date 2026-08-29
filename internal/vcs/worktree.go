package vcs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// vcsWaitDelay bounds the wait for inherited pipes after a git child exits
// or its context ends. git worktree add runs post-checkout hooks and smudge
// filters, and git push runs ssh and credential helpers; all inherit the
// pipe, so Wait can outlive the process without this.
const vcsWaitDelay = 5 * time.Second

const defaultWorktreeBranchPrefix = "mivia/"

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
	return CreateWithPrefix(ctx, repoRoot, name, baseRef, defaultWorktreeBranchPrefix)
}

// CreateWithPrefix adds a new worktree under workspace.WorktreesDir(repoRoot).
// It creates the branch branchPrefix plus the sanitised worktree name.
// baseRef is the branch, tag, or SHA to check out.
func CreateWithPrefix(ctx context.Context, repoRoot string, name string, baseRef string, branchPrefix string) (*WorktreeInfo, error) {
	return CreateWithPrefixLease(ctx, repoRoot, name, baseRef, branchPrefix, nil)
}

// CreateWithPrefixLease keeps lease open in the Git mutation process.
func CreateWithPrefixLease(ctx context.Context, repoRoot string, name string, baseRef string, branchPrefix string, lease *os.File) (*WorktreeInfo, error) {
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
	if err := validateWorktreeBranchPrefix(branchPrefix, sanitised); err != nil {
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
	// Prune a stale registration for this name before adding. A checkout
	// removed outside Remove (orphan cleanup) stays registered in
	// .git/worktrees/<name>/ until the stale entry is cleared, and the stale
	// registration makes `git worktree add` fail for the same name, wedging
	// the name permanently. Clearing it first lets the add re-create the
	// worktree and re-attach the retained branch; prune never deletes refs,
	// so retained-branch behavior is preserved. Unlike `git worktree prune`
	// (which drops any registration whose on-disk .git gitfile is missing,
	// even when the working tree is intact), the targeted prune removes the
	// registration only when its recorded working-tree directory is gone.
	if err := pruneStaleWorktree(ctx, root, sanitised); err != nil {
		return nil, err
	}
	// If the managed branch exists, attach it without changing its tip. This
	// preserves work that remains on the branch after a worktree is removed.
	// Otherwise create the branch from baseRef, or HEAD when baseRef is empty.
	ref := baseRef
	if ref == "" {
		ref = "HEAD"
	}
	branchName := branchPrefix + sanitised
	branchExists, err := localBranchExists(ctx, root, branchName)
	if err != nil {
		return nil, err
	}
	args := worktreeAddArgs(branchExists, branchName, ref, targetPath)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = vcsWaitDelay
	cmd.Dir = root
	cmd.Env = pinnedEnv()
	if out, err := runGitMutation(cmd, lease); err != nil {
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

// worktreeAddArgs builds the `git worktree add` invocation for a new run
// tree.
//
// The leading core.autocrlf=false is byte-faithful-checkout policy, not
// style: pinnedEnv deliberately keeps the user's global git config, and on
// windows-latest that sets core.autocrlf=true - so every newline-bearing
// tracked file materializes CRLF. Delivery later reads these trees
// byte-for-byte under GIT_CONFIG_GLOBAL=/dev/null (delivery.GitContext),
// where no conversion applies; a converted tree then differs from its
// committed blobs and every staged-diff guard reports phantom edits of
// untouched files (run 33052537404: chunk scope refused seeded fixture files
// as out-of-slice). Pin conversion off at the one place mivia creates a
// working tree.
func worktreeAddArgs(branchExists bool, branchName, ref, targetPath string) []string {
	args := []string{"-c", "core.autocrlf=false", "worktree", "add", targetPath}
	if branchExists {
		args = append(args, branchName)
	} else {
		args = append(args, "-b", branchName, ref)
	}
	return args
}

// localBranchExists reports whether branchName is an exact local branch.
func localBranchExists(ctx context.Context, root, branchName string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	cmd.WaitDelay = vcsWaitDelay
	cmd.Dir = root
	cmd.Env = pinnedEnv()
	if err := cmd.Run(); err == nil {
		return true, nil
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, &gitCommandError{cmd: "show-ref", err: err}
	}
}

// Remove deletes a worktree by name and clears a stale registration for the
// same name when its recorded working-tree directory is gone. It never drops
// a registration whose working tree is intact. It preserves all branches.
func Remove(ctx context.Context, repoRoot string, name string) error {
	return RemoveWithPrefix(ctx, repoRoot, name, defaultWorktreeBranchPrefix)
}

// RemoveWithPrefix deletes a worktree by name and clears a stale registration
// for the same name when its recorded working-tree directory is gone; a
// registration whose working tree is intact is never dropped. It preserves all
// branches. This avoids a race when another process changes the worktree
// branch during removal.
func RemoveWithPrefix(ctx context.Context, repoRoot string, name string, branchPrefix string) error {
	return RemoveWithPrefixLease(ctx, repoRoot, name, branchPrefix, nil)
}

// RemoveWithPrefixLease keeps lease open in the Git mutation process.
func RemoveWithPrefixLease(ctx context.Context, repoRoot string, name string, branchPrefix string, lease *os.File) error {
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
	if err := validateWorktreeBranchPrefix(branchPrefix, sanitised); err != nil {
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
	cmd.WaitDelay = vcsWaitDelay
	cmd.Dir = root
	cmd.Env = pinnedEnv()
	if out, err := runGitMutation(cmd, lease); err != nil {
		return &gitCommandError{cmd: "worktree remove", output: string(out), err: err}
	}
	// Best-effort cleanup of a same-name stale registration. After a
	// successful `git worktree remove` the registration is already gone and
	// this is a no-op; when git left a same-name entry behind (for example
	// an orphan whose directory vanished), the targeted prune clears it only
	// when its recorded working-tree directory is confirmed gone. An intact
	// worktree is never dropped, so this cannot remove a live registration.
	_ = pruneStaleWorktree(ctx, root, sanitised)

	return nil
}

// Prune drops the Git worktree registration for name, but only when the
// registered working-tree directory no longer exists. An intact working tree
// is never dropped, even when its on-disk .git gitfile is broken or missing:
// discoverability of a live worktree outranks clearing a stale entry. The
// orphan removal path calls it after RemoveWithPrefixLease reports a missing
// directory, so the stale entry disappears from the worktree list.
func Prune(ctx context.Context, repoRoot string, name string) error {
	root, _ := filepath.Abs(repoRoot) // Abs only fails if Getwd fails; git errors otherwise
	if err := ensureGitRepo(root); err != nil {
		return err
	}
	return pruneStaleWorktree(ctx, root, name)
}

func runGitMutation(cmd *exec.Cmd, lease *os.File) ([]byte, error) {
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cleanup, err := startProcessWithLease(cmd, lease)
	if err != nil {
		return output.Bytes(), err
	}
	cleanup()
	err = cmd.Wait()
	return output.Bytes(), err
}

// validateWorktreeBranchPrefix validates the complete branch name before it
// reaches Git. The worktree name comes from SanitizeName and only adds safe
// letters, digits, and hyphens.
func validateWorktreeBranchPrefix(prefix, sanitisedName string) error {
	if prefix == "" {
		return fmt.Errorf("worktree branch prefix must not be empty")
	}
	if !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("worktree branch prefix must end with /")
	}
	branchName := prefix + sanitisedName
	if strings.HasPrefix(branchName, "-") || strings.Contains(branchName, "@{") ||
		strings.Contains(branchName, "..") || strings.Contains(branchName, "//") ||
		strings.HasSuffix(branchName, ".") {
		return fmt.Errorf("worktree branch prefix %q is not a valid Git ref", prefix)
	}
	for _, r := range branchName {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("~^:?*[\\", r) {
			return fmt.Errorf("worktree branch prefix %q is not a valid Git ref", prefix)
		}
	}
	for _, component := range strings.Split(branchName, "/") {
		if component == "" || component == "." || component == ".." ||
			strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("worktree branch prefix %q is not a valid Git ref", prefix)
		}
	}
	return nil
}

// List returns all mivia-managed worktrees for the repo at repoRoot.
// The main worktree is filtered out.
func List(ctx context.Context, repoRoot string) ([]WorktreeInfo, error) {
	root, _ := filepath.Abs(repoRoot) // Abs only fails if Getwd fails; git errors otherwise
	// Expand Windows 8.3 short names before comparing: git prints the long
	// form, so a short-form root would otherwise produce a prefix that no
	// listed worktree path starts with and List would silently return none.
	root = workspace.LongPath(root)
	if err := ensureGitRepo(root); err != nil {
		return nil, err
	}
	wtDir := workspace.WorktreesDir(root)
	wtPrefix := wtDir + string(filepath.Separator)
	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	cmd.WaitDelay = vcsWaitDelay
	cmd.Dir = root
	cmd.Env = pinnedEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, &gitCommandError{cmd: "worktree list", output: string(out), err: err}
	}
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
	cmd.WaitDelay = vcsWaitDelay
	cmd.Dir = dir
	cmd.Env = pinnedEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", NotGitRepoError{Dir: dir}
	}
	return mainWorktreeFromListing(string(out), dir)
}

// mainWorktreeFromListing returns the first "worktree " path from a porcelain
// listing; git names the main working tree first. Kept separate from
// MainRepoRoot so the no-match branch is unit-testable. Git for Windows
// prints paths with forward slashes and resolved (long-form) names, so the
// returned path is normalized to the native separator form before any
// filepath join or prefix comparison sees it.
func mainWorktreeFromListing(out, dir string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "worktree ") {
			return filepath.Clean(filepath.FromSlash(strings.TrimPrefix(line, "worktree "))), nil
		}
	}
	return "", NotGitRepoError{Dir: dir}
}

// RepoRoot finds the git repository root from any directory inside it.
func RepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.WaitDelay = vcsWaitDelay
	cmd.Dir = dir
	cmd.Env = pinnedEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", NotGitRepoError{Dir: dir}
	}
	// Git prints forward slashes on every platform; normalize to the native
	// form so callers join and compare it against local paths.
	return filepath.Clean(filepath.FromSlash(strings.TrimSpace(string(out)))), nil
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
	wtDir := workspace.LongPath(workspace.WorktreesDir(root))
	abs, _ := filepath.Abs(dir)
	abs = workspace.LongPath(abs)
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
	cmd.WaitDelay = vcsWaitDelay
	cmd.Dir = dir
	cmd.Env = pinnedEnv()
	if err := cmd.Run(); err != nil {
		return NotGitRepoError{Dir: dir}
	}
	return nil
}

// Pruning and revision helpers (pruneStaleWorktree, workingTreePath,
// worktreePathFromGitdir, CurrentBranch, CurrentCommit, ResolveCommit,
// IsAncestor) live in prune.go and revision.go.

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
				// Git for Windows prints forward-slash, long-form paths;
				// normalize to the native form so the prefix comparison
				// below and filepath.Base agree with local joins.
				wt.path = filepath.Clean(filepath.FromSlash(val))
			case "branch":
				wt.branch = strings.TrimPrefix(val, "refs/heads/")
			case "HEAD":
				// Detached HEAD — branch field stays empty
			}
		}
	}
	// Flush the final block when the input does not end with a blank line.
	if wt.path != "" && strings.HasPrefix(wt.path, wtPrefix) {
		name := filepath.Base(wt.path)
		absPath, _ := filepath.Abs(wt.path)
		result = append(result, WorktreeInfo{
			Name:   name,
			Path:   absPath,
			Branch: wt.branch,
		})
	}
	return result, nil
}
