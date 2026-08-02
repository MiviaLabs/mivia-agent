//go:build unix

package tools

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenRegularFileRestoresBlockingMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "regular.txt")
	if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, info, err := openRegularFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !info.Mode().IsRegular() {
		t.Fatalf("opened mode = %s, want regular", info.Mode())
	}
	flags, err := fcntlGetFl(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if flags&syscall.O_NONBLOCK != 0 {
		t.Fatal("regular file retained O_NONBLOCK after open")
	}
	if err := fcntlSetFl(int(f.Fd()), flags); err != nil {
		t.Fatal(err)
	}
}

func TestUnixFcntlWrappersRejectInvalidDescriptor(t *testing.T) {
	if _, err := fcntlGetFl(-1); err == nil {
		t.Fatal("fcntlGetFl accepted an invalid descriptor")
	}
	if err := fcntlSetFl(-1, 0); err == nil {
		t.Fatal("fcntlSetFl accepted an invalid descriptor")
	}
	file, err := os.CreateTemp(t.TempDir(), "closed-file")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := clearNonblock(file); err == nil {
		t.Fatal("clearNonblock accepted an invalid descriptor")
	}
}
