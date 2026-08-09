package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// runWorktree handles worktree CLI commands.
func runWorktree(args []string) error {
	return runWorktreeWithIO(args, os.Stdout)
}

func runWorktreeWithIO(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("worktree: expected create, list, remove, or adopt")
	}

	switch args[0] {
	case "create":
		return runWorktreeCreate(args[1:], stdout)
	case "list":
		return runWorktreeList(args[1:], stdout)
	case "remove":
		return runWorktreeRemove(args[1:], stdout)
	case "adopt":
		return runWorktreeAdopt(args[1:], stdout)
	default:
		return fmt.Errorf("worktree: unknown subcommand %q (try create, list, remove, or adopt)", args[0])
	}
}

func runWorktreeAdopt(args []string, stdout io.Writer) error {
	workspaceDir, _, args, err := parseWorktreeCommandArgs(args, false)
	if err != nil {
		return fmt.Errorf("worktree adopt: %w", err)
	}
	if len(args) != 1 {
		return fmt.Errorf("worktree adopt: expected exactly one name")
	}
	repoRoot, err := worktreeCommandRoot(workspaceDir)
	if err != nil {
		return fmt.Errorf("worktree adopt: %w", err)
	}
	worktree, err := vcs.Resolve(context.Background(), repoRoot, args[0])
	if err != nil {
		return fmt.Errorf("worktree adopt: %w", err)
	}
	if worktree == nil {
		return fmt.Errorf("worktree adopt: worktree %q not found", args[0])
	}
	if _, err := adoptManagedWorktree(repoRoot, worktree); err != nil {
		return fmt.Errorf("worktree adopt: %w", err)
	}
	fmt.Fprintf(stdout, "adopted worktree %q at %s\n", worktree.Name, worktree.Path)
	return nil
}

func runWorktreeCreate(args []string, stdout io.Writer) error {
	workspaceDir, baseRef, args, err := parseWorktreeCommandArgs(args, true)
	if err != nil {
		return fmt.Errorf("worktree create: %w", err)
	}
	if len(args) != 1 {
		return fmt.Errorf("worktree create: expected exactly one name")
	}

	repoRoot, err := worktreeCommandRoot(workspaceDir)
	if err != nil {
		return fmt.Errorf("worktree create: %w", err)
	}
	worktreeConfig, err := config.LoadWorktreeConfig(repoRoot)
	if err != nil {
		return fmt.Errorf("worktree create: %w", err)
	}
	worktree, err := createManagedWorktree(repoRoot, args[0], baseRef, worktreeConfig.BranchPrefix)
	if err != nil {
		var exists vcs.WorktreeExistsError
		if errors.As(err, &exists) {
			return fmt.Errorf("worktree create: %w; use worktree adopt NAME for an existing worktree", err)
		}
		return fmt.Errorf("worktree create: %w", err)
	}
	fmt.Fprintf(stdout, "created worktree %q at %s\n", worktree.Name, worktree.Path)
	return nil
}

func runWorktreeList(args []string, stdout io.Writer) error {
	workspaceDir, _, args, err := parseWorktreeCommandArgs(args, false)
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	if len(args) != 0 {
		return fmt.Errorf("worktree list: expected no positional arguments")
	}

	repoRoot, err := worktreeCommandRoot(workspaceDir)
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	if _, err := config.LoadWorktreeConfig(repoRoot); err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	worktrees, err := vcs.List(context.Background(), repoRoot)
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	store, err := openRepositoryContextStore(repoRoot)
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	defer store.Close()
	principal, err := worktreeRoutePrincipal(repoRoot)
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	deleting, err := store.ListDeletingWorktreeInstances(context.Background(), principal)
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	writeWorktreeList(stdout, worktrees, deleting)
	return nil
}

func writeWorktreeList(stdout io.Writer, worktrees []vcs.WorktreeInfo, deleting []contextstate.WorktreeInstanceInfo) {
	written := make([]bool, len(deleting))
	for _, worktree := range worktrees {
		matched := -1
		canonicalPath, err := canonicalMarkerRoot(worktree.Path)
		if err == nil {
			for index, info := range deleting {
				if info.Instance.Worktree == worktree.Name && info.CanonicalPath == canonicalPath {
					matched = index
					break
				}
			}
		}
		if matched >= 0 {
			written[matched] = true
			fmt.Fprintf(stdout, "%s\trecovery required\t%s\n", worktree.Name, canonicalPath)
			continue
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", worktree.Name, worktree.Branch, worktree.Path)
	}
	for index, info := range deleting {
		if !written[index] {
			fmt.Fprintf(stdout, "%s\trecovery required\t%s\n", info.Instance.Worktree, info.CanonicalPath)
		}
	}
}

func runWorktreeRemove(args []string, stdout io.Writer) error {
	workspaceDir, _, args, err := parseWorktreeCommandArgs(args, false)
	if err != nil {
		return fmt.Errorf("worktree remove: %w", err)
	}
	if len(args) != 1 {
		return fmt.Errorf("worktree remove: expected exactly one name")
	}

	repoRoot, err := worktreeCommandRoot(workspaceDir)
	if err != nil {
		return fmt.Errorf("worktree remove: %w", err)
	}
	worktreeConfig, err := config.LoadWorktreeConfig(repoRoot)
	if err != nil {
		return fmt.Errorf("worktree remove: %w", err)
	}
	lock, err := lockWorktreeLifecycle(repoRoot, args[0])
	if err != nil {
		return fmt.Errorf("worktree remove: lock lifecycle: %w", err)
	}
	defer lock.Close()
	if recovered, err := recoverManagedWorktreeRemovalLocked(repoRoot, args[0], worktreeConfig.BranchPrefix, lock.File()); err != nil {
		return fmt.Errorf("worktree remove: recovery: %w", err)
	} else if recovered {
		fmt.Fprintf(stdout, "removed worktree %q\n", args[0])
		return nil
	}
	worktree, err := vcs.Resolve(context.Background(), repoRoot, args[0])
	if err != nil {
		return fmt.Errorf("worktree remove: %w", err)
	}
	if worktree == nil {
		// The Git worktree is gone, but storage may still own rows for the
		// name (a launch route or a live instance). Clean them so the zombie
		// row disappears from the session list too.
		cleaned, err := cleanupStaleWorktreeStorage(repoRoot, args[0])
		if err != nil {
			return fmt.Errorf("worktree remove: %w", err)
		}
		if cleaned {
			sanitized, sanitizeErr := vcs.SanitizeName(args[0])
			if sanitizeErr != nil {
				return fmt.Errorf("worktree remove: %w", sanitizeErr)
			}
			fmt.Fprintf(stdout, "removed worktree %q\n", sanitized)
			return nil
		}
		return fmt.Errorf("worktree remove: worktree %q not found", args[0])
	}
	if worktreeContainsCurrentDir(worktree.Path) {
		return fmt.Errorf("worktree remove: cannot remove the current worktree")
	}
	instance, err := beginManagedWorktreeRemoval(repoRoot, worktree)
	if errors.Is(err, errUnmanagedWorktree) {
		// The worktree has no valid lifecycle binding (missing marker or no
		// storage entry). Remove it directly so its HDD space is freed.
		if err := removeUnmanagedWorktree(repoRoot, worktree, worktreeConfig.BranchPrefix, lock.File()); err != nil {
			return fmt.Errorf("worktree remove: %w", err)
		}
		fmt.Fprintf(stdout, "removed worktree %q\n", worktree.Name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("worktree remove: begin session cleanup: %w", err)
	}
	if err := vcs.RemoveWithPrefixLease(context.Background(), repoRoot, args[0], worktreeConfig.BranchPrefix, lock.File()); err != nil {
		if reactivateErr := reactivateManagedWorktree(repoRoot, instance); reactivateErr != nil {
			return fmt.Errorf("worktree remove: %w; session lifecycle recovery failed: %v", err, reactivateErr)
		}
		return fmt.Errorf("worktree remove: %w", err)
	}
	if err := finishManagedWorktreeRemoval(repoRoot, instance); err != nil {
		return fmt.Errorf("worktree remove: removed %q but could not clean its sessions: %w", worktree.Name, err)
	}
	fmt.Fprintf(stdout, "removed worktree %q\n", worktree.Name)
	return nil
}

func worktreeCommandRoot(workspaceDir string) (string, error) {
	if workspaceDir == "" {
		workspaceDir = "."
	}
	abs, err := filepath.Abs(workspaceDir)
	if err != nil {
		return "", err
	}
	return vcs.MainRepoRoot(abs)
}

func parseWorktreeCommandArgs(args []string, allowBranch bool) (string, string, []string, error) {
	var workspaceDir string
	var baseRef string
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--workspace":
			value, next, err := worktreeFlagValue(args, i, "--workspace")
			if err != nil {
				return "", "", nil, err
			}
			workspaceDir = value
			i = next
		case strings.HasPrefix(arg, "--workspace="):
			workspaceDir = strings.TrimPrefix(arg, "--workspace=")
			if workspaceDir == "" {
				return "", "", nil, fmt.Errorf("missing value for --workspace")
			}
		case arg == "--branch":
			if !allowBranch {
				return "", "", nil, fmt.Errorf("unknown flag --branch")
			}
			value, next, err := worktreeFlagValue(args, i, "--branch")
			if err != nil {
				return "", "", nil, err
			}
			baseRef = value
			i = next
		case strings.HasPrefix(arg, "--branch="):
			if !allowBranch {
				return "", "", nil, fmt.Errorf("unknown flag --branch")
			}
			baseRef = strings.TrimPrefix(arg, "--branch=")
			if baseRef == "" {
				return "", "", nil, fmt.Errorf("missing value for --branch")
			}
		case strings.HasPrefix(arg, "--"):
			return "", "", nil, fmt.Errorf("unknown flag %s", arg)
		default:
			positional = append(positional, arg)
		}
	}
	return workspaceDir, baseRef, positional, nil
}

func worktreeFlagValue(args []string, index int, name string) (string, int, error) {
	if index+1 == len(args) || strings.HasPrefix(args[index+1], "--") {
		return "", index, fmt.Errorf("missing value for %s", name)
	}
	return args[index+1], index + 1, nil
}
