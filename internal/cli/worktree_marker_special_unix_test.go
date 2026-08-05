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
