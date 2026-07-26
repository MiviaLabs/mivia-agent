package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenAndResolveNested(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "f.txt"), []byte("deep"), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(ws.Abs) {
		t.Fatalf("Abs not absolute: %s", ws.Abs)
	}

	p, err := ws.Resolve("a/b/c/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !isUnder(ws.Abs, p) {
		t.Fatalf("resolved path not under root: %s", p)
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "deep" {
		t.Fatalf("read via resolve: %q err=%v", data, err)
	}

	// Dot segments that stay inside.
	p2, err := ws.Resolve("a/../a/b/./c/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !isUnder(ws.Abs, p2) {
		t.Fatalf("dot-resolved path escaped: %s", p2)
	}
}

func TestResolveEscapeVariants(t *testing.T) {
	dir := t.TempDir()
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Relative escape.
	if _, err := ws.Resolve("../outside"); err == nil {
		t.Fatal("expected ../outside to fail")
	}
	if _, err := ws.Resolve("a/../../outside"); err == nil {
		t.Fatal("expected a/../../outside to fail")
	}

	// Absolute path outside workspace.
	outside := filepath.Join(os.TempDir(), "mivia-ws-escape-"+t.Name())
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	if _, err := ws.Resolve(outside); err == nil {
		t.Fatal("expected absolute outside path to fail")
	}

	if runtime.GOOS != "windows" {
		if _, err := ws.Resolve("/etc/passwd"); err == nil {
			t.Fatal("expected /etc/passwd to fail")
		}
	}
}

func TestResolveNewNestedPath(t *testing.T) {
	dir := t.TempDir()
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ws.Resolve("sub/dir/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !isUnder(ws.Abs, p) {
		t.Fatalf("not under root: %s", p)
	}
	// Parent of non-existent file must still be under root after join.
	if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(ws.Abs)) {
		t.Fatalf("prefix check failed: root=%s path=%s", ws.Abs, p)
	}
}

func TestOpenRejectsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "notadir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(f); err == nil {
		t.Fatal("expected error opening file as workspace")
	}
}

func TestRel(t *testing.T) {
	dir := t.TempDir()
	ws, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(ws.Abs, "x", "y.txt")
	rel := ws.Rel(abs)
	if rel != filepath.Join("x", "y.txt") && rel != "x/y.txt" {
		// Windows vs unix — accept either separator form.
		if !strings.Contains(rel, "y.txt") {
			t.Fatalf("rel=%q", rel)
		}
	}
}

func TestIsUnder(t *testing.T) {
	if !isUnder("/ws", "/ws") {
		t.Fatal("root should be under itself")
	}
	if !isUnder("/ws", "/ws/a") {
		t.Fatal("child should be under")
	}
	if isUnder("/ws", "/ws2/a") {
		t.Fatal("sibling prefix must not match")
	}
	if isUnder("/ws", "/tmp") {
		t.Fatal("other tree must not match")
	}
}
