//go:build unix

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Regression (DC-10 boundary): openRegularFileWrite must refuse a symlink at
// the final component. Root.Resolve already canonicalizes legitimate
// final-component symlinks, so a symlink can only reach the open through a
// swap between Resolve and the open - and O_NOFOLLOW turns that swap into a
// hard ELOOP refusal instead of letting write_file/search_replace O_TRUNC a
// file outside the workspace. Before O_NOFOLLOW this test FAILED: the open
// followed the link, f.Stat() reported a regular file, and the outside target
// was truncated.
func TestOpenRegularFileWriteRefusesSymlinkFinalComponent(t *testing.T) {
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "swap.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}

	_, _, err := openRegularFileWrite(link, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err == nil {
		t.Fatal("openRegularFileWrite followed a symlink final component; expected refusal")
	}
	if !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("refusal error = %v, want ELOOP", err)
	}
	data, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "original" {
		t.Fatalf("outside file was modified through the symlink: %q", data)
	}
}

// Read side of the same boundary: the shared open helper must refuse a
// final-component symlink instead of streaming a file from outside the
// workspace.
func TestOpenRegularFileRefusesSymlinkFinalComponent(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "swap.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRegularFile(link); err == nil {
		t.Fatal("openRegularFile followed a symlink final component; expected refusal")
	}
}

// Positive companion: a plain regular file still opens and truncates.
func TestOpenRegularFileWriteOrdinaryFileStillWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _, err := openRegularFileWrite(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("after")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after" {
		t.Fatalf("content = %q, want %q", data, "after")
	}
}

// Defense in depth: Resolve is the FIRST line of defense - write_file to a
// final-component symlink pointing outside is refused with the escapes
// workspace error before any open happens. Passes before and after the
// O_NOFOLLOW change; O_NOFOLLOW closes only the post-Resolve swap window.
func TestWriteFileSymlinkEscapeRefusedByResolve(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})
	out, err := reg.Execute(context.Background(), "write_file",
		json.RawMessage(`{"path":"link.txt","content":"pwned"}`))
	if err == nil {
		t.Fatalf("write_file through an outside symlink succeeded: %s", out)
	}
	if !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("error = %v, want escapes workspace", err)
	}
	data, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "original" {
		t.Fatalf("outside file modified: %q", data)
	}
}
