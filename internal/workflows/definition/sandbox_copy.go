package definition

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/secretpath"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func copySandboxWorktree(source, destination string, policy secretpath.Policy) (string, error) {
	return copySandboxTree(source, destination, policy)
}

func copySandboxTree(source, destination string, policy secretpath.Policy) (string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", fmt.Errorf("inspect verifier worktree: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("verifier worktree is not a directory")
	}
	if err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(destination, 0o700)
		}
		if filepath.Base(rel) == ".git" || filepath.Base(rel) == ".codegraph" || sandboxControlDirectory(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if policy.Match(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, rel)
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("verifier worktree contains symlink %q", rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("verifier worktree contains unsupported file %q", rel)
		}
		return copyRegularFile(path, target)
	}); err != nil {
		return "", fmt.Errorf("copy verifier worktree: %w", err)
	}
	return destination, nil
}

func sandboxControlDirectory(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 2 || parts[1] != "worktrees" {
		return false
	}
	switch parts[0] {
	case ".agents", ".claude", ".codex", workspace.Namespace:
		return true
	default:
		return false
	}
}

func copyRegularFile(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
