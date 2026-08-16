package vcs

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// CurrentBranch returns the currently checked-out branch name for a worktree.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	cmd.Env = pinnedEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CurrentCommit returns the commit at HEAD in dir.
func CurrentCommit(ctx context.Context, dir string) (string, error) {
	return ResolveCommit(ctx, dir, "HEAD")
}

// ResolveCommit resolves ref to an exact commit.
func ResolveCommit(ctx context.Context, dir, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("resolve commit: ref must not be empty")
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	cmd.Dir = dir
	cmd.Env = pinnedEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &gitCommandError{cmd: "rev-parse", output: string(out), err: err}
	}
	return strings.TrimSpace(string(out)), nil
}

// IsAncestor reports whether ancestor is an ancestor of descendant.
func IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	ancestorCommit, err := ResolveCommit(ctx, dir, ancestor)
	if err != nil {
		return false, err
	}
	descendantCommit, err := ResolveCommit(ctx, dir, descendant)
	if err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestorCommit, descendantCommit)
	cmd.Dir = dir
	cmd.Env = pinnedEnv()
	if err := cmd.Run(); err == nil {
		return true, nil
	} else {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, &gitCommandError{cmd: "merge-base --is-ancestor", err: err}
	}
}
