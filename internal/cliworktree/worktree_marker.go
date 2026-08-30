package cliworktree

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// worktreeMarkerWaitDelay bounds the wait for pipes a grandchild still holds after the
// child exits. Without it, Wait blocks on the pipe rather than the process.
const worktreeMarkerWaitDelay = 5 * time.Second

const worktreeMarkerName = "worktree-instance.json"

const maxWorktreeMarkerBytes = 4096

const worktreeMarkerExclude = "/" + workspace.Namespace + "/" + worktreeMarkerName

type worktreeMarker struct {
	Version  int    `json:"version"`
	Worktree string `json:"worktree"`
	ID       string `json:"id"`
}

var (
	lockWorktreeMarkerForUpdate = LockWorktreeMarkerFile
	writeWorktreeMarkerTemp     = func(file *os.File, content []byte) (int, error) { return file.Write(content) }
	closeWorktreeMarkerTemp     = func(file *os.File) error { return file.Close() }
	renameWorktreeMarker        = os.Rename
	openWorktreeMarkerFile      = openWorktreeMarkerForRead
	lstatGitInfoDir             = func(root *os.Root, path string) (os.FileInfo, error) { return root.Lstat(path) }
	mkdirGitInfoDir             = func(root *os.Root, path string, mode os.FileMode) error { return root.Mkdir(path, mode) }
	openMarkerExcludeLock       = OpenMarkerExcludeLockFile
	writeWorktreeExcludeTemp    = func(file *os.File, content []byte) (int, error) { return file.Write(content) }
	closeWorktreeExcludeTemp    = func(file *os.File) error { return file.Close() }
	readWorktreeMarkerRandom    = rand.Read
	statWorktreeMarkerFile      = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
	readWorktreeMarkerFile      = func(reader io.Reader) ([]byte, error) { return io.ReadAll(reader) }
)

func WorktreeMarkerPath(root string) string {
	return workspace.NamespacePath(root, worktreeMarkerName)
}

func WriteWorktreeMarker(root string, instance contextstate.WorktreeInstance) error {
	canonical, err := CanonicalMarkerRoot(root)
	if err != nil {
		return err
	}
	if err := instance.Validate(); err != nil || instance.IsZero() {
		return fmt.Errorf("invalid worktree marker instance: %w", contextstate.ErrInvalidDTO)
	}
	dir := workspace.NamespacePath(canonical)
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
	if err := publishWorktreeMarker(name, WorktreeMarkerPath(canonical)); err != nil {
		return fmt.Errorf("publish worktree marker: %w", err)
	}
	return nil
}

// publishWorktreeMarker renames the temp file over the marker path. On
// Windows a rename over an existing file that another writer is renaming
// onto (or holds open) fails with ERROR_ACCESS_DENIED, so concurrent
// idempotent publishes race each other into spurious errors; a short
// bounded retry absorbs that transient contention. Other platforms rename
// atomically and never retry.
func publishWorktreeMarker(name, target string) error {
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if err = renameWorktreeMarker(name, target); err == nil {
			return nil
		}
		if runtime.GOOS != "windows" {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
	}
	return err
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
	cmd.WaitDelay = worktreeMarkerWaitDelay
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
	info, err := lstatGitInfoDir(root, "info")
	if os.IsNotExist(err) {
		if mkdirErr := mkdirGitInfoDir(root, "info", 0700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return fmt.Errorf("create Git info directory: %w", mkdirErr)
		}
		info, err = lstatGitInfoDir(root, "info")
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
	file, err := openMarkerExcludeLock(root, path)
	if err != nil {
		return nil, fmt.Errorf("open Git exclude lock: %w", err)
	}
	info, err := statWorktreeMarkerFile(file)
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect Git exclude lock: %w", err)
		}
		return nil, fmt.Errorf("Git exclude lock is not a regular file")
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

// ReadWorktreeMarker implements read worktree marker.
func ReadWorktreeMarker(root string) (contextstate.WorktreeInstance, error) {
	canonical, err := CanonicalMarkerRoot(root)
	if err != nil {
		return contextstate.WorktreeInstance{}, err
	}
	markerRoot, err := os.OpenRoot(canonical)
	if err != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("open worktree marker root: %w", err)
	}
	defer markerRoot.Close()
	if info, statErr := markerRoot.Lstat(workspace.Namespace); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return contextstate.WorktreeInstance{}, fmt.Errorf("worktree marker directory is a symlink")
	} else if statErr != nil {
		return contextstate.WorktreeInstance{}, fmt.Errorf("inspect worktree marker directory: %w", statErr)
	}
	markerPath := filepath.Join(workspace.Namespace, worktreeMarkerName)
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

func CanonicalMarkerRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// Expand Windows 8.3 short names so the raw and the resolved path use
	// the same rendering; otherwise a short-name path (for example under a
	// TEMP directory that the OS created with a short user name) is
	// indistinguishable from a symlink redirect.
	abs = workspace.LongPath(abs)
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve worktree root: %w", err)
	}
	if filepath.Clean(abs) != filepath.Clean(canonical) {
		return "", fmt.Errorf("worktree root must be canonical")
	}
	return canonical, nil
}
