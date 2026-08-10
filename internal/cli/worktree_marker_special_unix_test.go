//go:build unix

package cli

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestWorktreeMarkerRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := worktreeMarkerPath(root)
	if err := syscall.Mkfifo(markerPath, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := readWorktreeMarker(root)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("FIFO marker was accepted")
		}
	case <-time.After(200 * time.Millisecond):
		writer, err := os.OpenFile(markerPath, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_ = writer.Close()
		<-result
		t.Fatal("FIFO marker read blocked")
	}
}

func TestWorktreeMarkerRejectsSocket(t *testing.T) {
	base := "/tmp"
	if runtime.GOOS == "darwin" {
		base = "/private/tmp"
	}
	root, err := os.MkdirTemp(base, "mivia-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Mkdir(filepath.Join(root, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: worktreeMarkerPath(root), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := readWorktreeMarker(root); err == nil {
		t.Fatal("socket marker was accepted")
	}
}

func TestWorktreeMarkerExcludeLockRejectsFifo(t *testing.T) {
	repo := newWorktreeCommandRepo(t)
	commonDir, err := worktreeGitCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(commonDir, "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	lockPath := filepath.Join("info", "exclude.lock")
	if err := syscall.Mkfifo(filepath.Join(commonDir, lockPath), 0o600); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockWorktreeMarkerExclude(root, lockPath)
	if unlock != nil {
		unlock()
	}
	if err == nil {
		t.Fatal("Git exclude lock opened a FIFO")
	}
}

func TestWorktreeMarkerRejectsFinalComponentSymlinkAtOpen(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, ".mivia"), 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := worktreeMarkerPath(rootPath)
	// Relative link target inside the marker directory, matching the
	// write-through reproduction that proved os.Root.OpenFile follows
	// final-component symlinks.
	target := filepath.Join(".mivia", "target")
	if err := os.WriteFile(filepath.Join(rootPath, target), []byte(`{"version":1,"worktree":"wt-a","id":"wt_1234567890abcdef"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", markerPath); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openWorktreeMarkerForRead(root, filepath.Join(".mivia", worktreeMarkerName))
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("marker read followed a final-component symlink")
	}
}

func TestWorktreeMarkerRejectsSymlinkMarkerDirectoryAtOpen(t *testing.T) {
	rootPath := t.TempDir()
	target := filepath.Join(rootPath, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(rootPath, ".mivia")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openWorktreeMarkerForRead(root, filepath.Join(".mivia", worktreeMarkerName))
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("marker read followed a symlinked marker directory")
	}
}

func TestWorktreeMarkerReadClosedRoot(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openWorktreeMarkerForRead(root, filepath.Join(".mivia", worktreeMarkerName)); err == nil {
		t.Fatal("closed root read the marker")
	}
}

func TestWorktreeMarkerExcludeLockRejectsSymlinkInfoDir(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Remove(filepath.Join(base, "info")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, "info")); err != nil {
		t.Fatal(err)
	}
	file, err := openMarkerExcludeLock(root, filepath.Join("info", "exclude.lock"))
	if file != nil {
		_ = file.Close()
	}
	if err == nil {
		t.Fatal("Git exclude lock opened through a symlinked info directory")
	}
}
