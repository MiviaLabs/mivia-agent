package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
)

const worktreeMarkerName = "worktree-instance.json"

const maxWorktreeMarkerBytes = 4096

const worktreeMarkerExclude = "/.mivia/" + worktreeMarkerName

type worktreeMarker struct {
	Version  int    `json:"version"`
	Worktree string `json:"worktree"`
	ID       string `json:"id"`
}

var (
	lockWorktreeMarkerForUpdate = lockWorktreeMarkerFile
	writeWorktreeMarkerTemp     = func(file *os.File, content []byte) (int, error) { return file.Write(content) }
	closeWorktreeMarkerTemp     = func(file *os.File) error { return file.Close() }
	renameWorktreeMarker        = os.Rename
	openWorktreeMarkerFile      = openWorktreeMarkerForRead
	writeWorktreeExcludeTemp    = func(file *os.File, content []byte) (int, error) { return file.Write(content) }
	closeWorktreeExcludeTemp    = func(file *os.File) error { return file.Close() }
	readWorktreeMarkerRandom    = rand.Read
	statWorktreeMarkerFile      = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
	readWorktreeMarkerFile      = func(reader io.Reader) ([]byte, error) { return io.ReadAll(reader) }
)

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
	if err := ensureWorktreeMarkerExcluded(canonical); err != nil {
		return err
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
	if _, err := writeWorktreeMarkerTemp(temporary, data); err != nil {
		temporary.Close()
		return fmt.Errorf("write worktree marker: %w", err)
	}
	if err := closeWorktreeMarkerTemp(temporary); err != nil {
		return fmt.Errorf("close worktree marker: %w", err)
	}
	if err := renameWorktreeMarker(name, worktreeMarkerPath(canonical)); err != nil {
		return fmt.Errorf("publish worktree marker: %w", err)
	}
	return nil
}

func ensureWorktreeMarkerExcluded(root string) error {
	commonDir, err := worktreeGitCommonDir(root)
	if err != nil {
		return err
	}
	gitRoot, err := os.OpenRoot(commonDir)
	if err != nil {
		return fmt.Errorf("open Git common directory: %w", err)
	}
	defer gitRoot.Close()
	if err := ensureRegularGitInfoDir(gitRoot); err != nil {
		return err
	}
	return updateWorktreeMarkerExclude(gitRoot, filepath.Join("info", "exclude"))
}

func worktreeGitCommonDir(root string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-common-dir")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve Git common directory: %w", err)
	}
	commonDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	commonDir, err = filepath.EvalSymlinks(filepath.Clean(commonDir))
	if err != nil {
		return "", fmt.Errorf("resolve Git common directory: %w", err)
	}
	return commonDir, nil
}

func ensureRegularGitInfoDir(root *os.Root) error {
	info, err := root.Lstat("info")
	if os.IsNotExist(err) {
		if err := root.Mkdir("info", 0700); err != nil {
			return fmt.Errorf("create Git info directory: %w", err)
		}
		info, err = root.Lstat("info")
	}
	if err != nil {
		return fmt.Errorf("inspect Git info directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("Git info path is not a regular directory")
	}
	return nil
}

func updateWorktreeMarkerExclude(root *os.Root, path string) error {
	unlock, err := lockWorktreeMarkerExclude(root, path+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	content, mode, err := readWorktreeMarkerExclude(root, path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(content), "\n") {
		if line == worktreeMarkerExclude {
			return nil
		}
	}
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	content = append(content, worktreeMarkerExclude...)
	content = append(content, '\n')
	return replaceWorktreeMarkerExclude(root, path, content, mode)
}

func lockWorktreeMarkerExclude(root *os.Root, path string) (func(), error) {
	if info, err := root.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("Git exclude lock is a symlink")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect Git exclude lock: %w", err)
	}
	file, err := root.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("open Git exclude lock: %w", err)
	}
	unlock, err := lockWorktreeMarkerForUpdate(file)
	if err != nil {
		file.Close()
		return nil, err
	}
	return func() {
		unlock()
		_ = file.Close()
	}, nil
}

func readWorktreeMarkerExclude(root *os.Root, path string) ([]byte, os.FileMode, error) {
	info, err := root.Lstat(path)
	if os.IsNotExist(err) {
		return nil, 0600, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("inspect Git exclude: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("Git exclude is not a regular file")
	}
	content, err := root.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read Git exclude: %w", err)
	}
	return content, info.Mode().Perm(), nil
}

func replaceWorktreeMarkerExclude(root *os.Root, path string, content []byte, mode os.FileMode) error {
	temporary, name, err := createWorktreeMarkerExcludeTemp(root, filepath.Dir(path), mode)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	if _, err := writeWorktreeExcludeTemp(temporary, content); err != nil {
		temporary.Close()
		return fmt.Errorf("write Git exclude: %w", err)
	}
	if err := closeWorktreeExcludeTemp(temporary); err != nil {
		return fmt.Errorf("close Git exclude: %w", err)
	}
	if err := root.Rename(name, path); err != nil {
		return fmt.Errorf("publish Git exclude: %w", err)
	}
	return nil
}

func createWorktreeMarkerExcludeTemp(root *os.Root, dir string, mode os.FileMode) (*os.File, string, error) {
	for range 10 {
		var random [8]byte
		if _, err := readWorktreeMarkerRandom(random[:]); err != nil {
			return nil, "", fmt.Errorf("name Git exclude temporary file: %w", err)
		}
		name := filepath.Join(dir, ".exclude-"+hex.EncodeToString(random[:]))
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err == nil {
			return file, name, nil
		}
		if !os.IsExist(err) {
			return nil, "", fmt.Errorf("create Git exclude: %w", err)
		}
	}
	return nil, "", fmt.Errorf("create Git exclude: temporary name collision")
}

func readWorktreeMarker(root string) (contextstate.WorktreeInstance, error) {
	canonical, err := canonicalMarkerRoot(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	markerRoot, err := os.OpenRoot(canonical)
	if err != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("open worktree marker root: %w", err)
	}
	defer markerRoot.Close()
	if info, statErr := markerRoot.Lstat(".mivia"); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree marker directory is a symlink")
	} else if statErr != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("inspect worktree marker directory: %w", statErr)
	}
	markerPath := filepath.Join(".mivia", worktreeMarkerName)
	if info, statErr := markerRoot.Lstat(markerPath); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree marker is not a regular file")
	} else if statErr != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("inspect worktree marker: %w", statErr)
	}
	file, err := openWorktreeMarkerFile(markerRoot, markerPath)
	if err != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("read worktree marker: %w", err)
	}
	defer file.Close()
	info, err := statWorktreeMarkerFile(file)
	if err != nil || !info.Mode().IsRegular() {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree marker is not a regular file")
	}
	if info.Size() > maxWorktreeMarkerBytes {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree marker is too large")
	}
	data, err := readWorktreeMarkerFile(io.LimitReader(file, maxWorktreeMarkerBytes+1))
	if err != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("read worktree marker: %w", err)
	}
	if len(data) > maxWorktreeMarkerBytes {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree marker is too large")
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
