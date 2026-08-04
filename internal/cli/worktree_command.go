package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/vcs"
)

// runWorktree handles worktree CLI commands.
func runWorktree(args []string) error {
	return runWorktreeWithIO(args, os.Stdout)
}

func runWorktreeWithIO(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("worktree: expected create, list, or remove")
	}

	switch args[0] {
	case "create":
		return runWorktreeCreate(args[1:], stdout)
	case "list":
		return runWorktreeList(args[1:], stdout)
	case "remove":
		return runWorktreeRemove(args[1:], stdout)
	default:
		return fmt.Errorf("worktree: unknown subcommand %q (try create, list, or remove)", args[0])
	}
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
	worktree, err := vcs.Create(context.Background(), repoRoot, args[0], baseRef)
	if err != nil {
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
	worktrees, err := vcs.List(context.Background(), repoRoot)
	if err != nil {
		return fmt.Errorf("worktree list: %w", err)
	}
	for _, worktree := range worktrees {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", worktree.Name, worktree.Branch, worktree.Path)
	}
	return nil
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
	worktree, err := vcs.Resolve(context.Background(), repoRoot, args[0])
	if err != nil {
		return fmt.Errorf("worktree remove: %w", err)
	}
	if worktree == nil {
		return fmt.Errorf("worktree remove: worktree %q not found", args[0])
	}
	if worktreeContainsCurrentDir(worktree.Path) {
		return fmt.Errorf("worktree remove: cannot remove the current worktree")
	}
	if err := vcs.Remove(context.Background(), repoRoot, args[0]); err != nil {
		return fmt.Errorf("worktree remove: %w", err)
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
