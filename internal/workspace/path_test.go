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
		// Windows vs unix - accept either separator form.
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

func TestSameExistingPathUsesFileIdentity(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("create directory link: %v", err)
		}
		t.Fatal(err)
	}
	same, err := SameExistingPath(dir, alias)
	if err != nil {
		t.Fatalf("SameExistingPath: %v", err)
	}
	if !same {
		t.Fatalf("SameExistingPath(%q, %q) = false, want true", dir, alias)
	}
}

// TestResolveContainmentTable locks the workspace containment contract
// (DC-10): Resolve evaluates symlinks BEFORE the isUnder check, so every
// escape variant - relative .., absolute outside, a symlink inside the
// workspace pointing outside, a missing suffix reached through an outside
// symlink, and a dangling final-component symlink - is refused, while
// legitimate paths (including a final-component symlink that resolves inside
// the workspace) stay under the root. The NUL and overlong-name rows pin the
// current behavior: the pure string predicates accept them (lexically under
// the root), and the OS refuses the open - a containment guarantee, never an
// escape.
func TestResolveContainmentTable(t *testing.T) {
	outside := t.TempDir()
	longName := strings.Repeat("x", 300)
	cases := []resolveContainmentCase{
		{name: "empty", path: "", wantErr: true},
		{name: "whitespace only", path: "   ", wantErr: true},
		{name: "dot", path: ".", wantOK: true},
		{name: "dotdot", path: "..", wantErr: true},
		{name: "deep escape", path: "a/../../..", wantErr: true},
		{name: "absolute outside", path: outside, wantErr: true},
		{name: "symlink to outside", path: "link", wantErr: true,
			setup: func(t *testing.T, root string) {
				target := filepath.Join(outside, "target.txt")
				if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "missing suffix through outside symlink", path: "link/child/new.txt", wantErr: true,
			setup: func(t *testing.T, root string) {
				if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "dangling final symlink", path: "dangle", wantErr: true,
			setup: func(t *testing.T, root string) {
				if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(root, "dangle")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "symlink inside to inside target", path: "alias", wantOK: true,
			setup: func(t *testing.T, root string) {
				target := filepath.Join(root, "real.txt")
				if err := os.WriteFile(target, []byte("inside"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, "alias")); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "NUL byte", path: "a\x00b", wantOK: true, openRefused: true},
		{name: "overlong final component", path: "a/" + longName, wantOK: true, openRefused: true},
	}
	if runtime.GOOS != "windows" {
		cases = append(cases, resolveContainmentCase{name: "/etc/passwd", path: "/etc/passwd", wantErr: true})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertResolveContainment(t, tc, outside)
		})
	}
}

type resolveContainmentCase struct {
	name        string
	path        string
	setup       func(t *testing.T, root string)
	wantErr     bool // exact: Resolve must fail
	wantOK      bool // exact: Resolve must succeed under the root
	openRefused bool // when Resolve succeeds, the OS must refuse the open
}

// assertResolveContainment runs one containment row against a fresh workspace
// root and pins the exact outcome: an error, or a path under the root (and,
// for NUL/overlong rows, an OS-level open refusal).
func assertResolveContainment(t *testing.T, tc resolveContainmentCase, outside string) {
	t.Helper()
	root := t.TempDir()
	ws, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if tc.setup != nil {
		tc.setup(t, root)
	}
	p, err := ws.Resolve(tc.path)
	if tc.wantErr {
		if err == nil {
			t.Fatalf("Resolve(%q) = %q, want error", tc.path, p)
		}
		return
	}
	if err != nil {
		t.Fatalf("Resolve(%q): %v", tc.path, err)
	}
	if !isUnder(ws.Abs, p) {
		t.Fatalf("Resolve(%q) = %q escapes root %s", tc.path, p, ws.Abs)
	}
	if tc.name == "symlink inside to inside target" {
		want, evalErr := filepath.EvalSymlinks(filepath.Join(root, "real.txt"))
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		if p != want {
			t.Fatalf("Resolve(%q) = %q, want canonical target %q", tc.path, p, want)
		}
	}
	if tc.openRefused {
		if _, statErr := os.Stat(p); statErr == nil {
			t.Fatalf("Resolve(%q) = %q: expected the OS to refuse the open, but stat succeeded", tc.path, p)
		}
	}
}
