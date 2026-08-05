//go:build unix

package cli

import (
	"net"
	"os"
	"path/filepath"
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
	root := t.TempDir()
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
