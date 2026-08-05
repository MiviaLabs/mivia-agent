package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

const worktreeMarkerName = "worktree-instance.json"

type worktreeMarker struct {
	Version  int    `json:"version"`
	Worktree string `json:"worktree"`
	ID       string `json:"id"`
}

func worktreeMarkerPath(root string) string {
	return filepath.Join(root, ".mivia", worktreeMarkerName)
}

func writeWorktreeMarker(root string, instance contextstate.WorktreeInstance) error {
	canonical, err := canonicalMarkerRoot(root)
	if err != nil {
		return err
	}
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return fmt.Errorf("invalid worktree marker instance: %w", contextstate.ErrInvalidDTO)
	}
	dir := filepath.Join(canonical, ".mivia")
	if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("worktree marker directory is a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect worktree marker directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create worktree marker directory: %w", err)
	}
	data, err := json.Marshal(worktreeMarker{Version: 1, Worktree: instance.Worktree, ID: instance.ID})
	if err != nil {
		return fmt.Errorf("encode worktree marker: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".worktree-instance-*")
	if err != nil {
		return fmt.Errorf("create worktree marker: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure worktree marker: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write worktree marker: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close worktree marker: %w", err)
	}
	if err := os.Rename(name, worktreeMarkerPath(canonical)); err != nil {
		return fmt.Errorf("publish worktree marker: %w", err)
	}
	return nil
}

func readWorktreeMarker(root string) (contextstate.WorktreeInstance, error) {
	canonical, err := canonicalMarkerRoot(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	dir := filepath.Join(canonical, ".mivia")
	if info, statErr := os.Lstat(dir); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree marker directory is a symlink")
	}
	markerPath := worktreeMarkerPath(canonical)
	if info, statErr := os.Lstat(markerPath); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree marker is a symlink")
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("read worktree marker: %w", err)
	}
	var marker worktreeMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("decode worktree marker: %w", err)
	}
	instance := contextstate.WorktreeInstance{Worktree: marker.Worktree, ID: marker.ID}
	if marker.Version != 1 || instance.IsZero() || instance.Validate() != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("invalid worktree marker: %w", contextstate.ErrInvalidDTO)
	}
	return instance, nil
}

func canonicalMarkerRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve worktree root: %w", err)
	}
	if filepath.Clean(abs) != filepath.Clean(canonical) {
		return "", fmt.Errorf("worktree root must be canonical")
	}
	return canonical, nil
}
