package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// mustOpen opens a fresh t.TempDir() as a workspace root.
func mustOpen(t *testing.T) *Root {
	t.Helper()
	ws, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return ws
}

// TestLexicalRelRelativeInputs pins LexicalRel's behavior for relative
// inputs: they are cleaned (dot segments collapsed, trailing slashes
// removed) and returned as-is, never resolved against the root.
func TestLexicalRelRelativeInputs(t *testing.T) {
	ws := mustOpen(t)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "repeated slash and dot segments", in: "a//b/./c", want: "a/b/c"},
		{name: "trailing slash", in: "protected/", want: "protected"},
		{name: "dot", in: ".", want: "."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ws.LexicalRel(tc.in)
			if err != nil {
				t.Fatalf("LexicalRel(%q): %v", tc.in, err)
			}
			if want := filepath.ToSlash(tc.want); got != want {
				t.Fatalf("LexicalRel(%q) = %q, want %q", tc.in, got, want)
			}
		})
	}
}

// TestLexicalRelAbsoluteInside maps an absolute path under the lexical root
// to its workspace-relative form without touching the filesystem.
func TestLexicalRelAbsoluteInside(t *testing.T) {
	ws := mustOpen(t)
	in := filepath.Join(ws.Abs, "protected", "x")
	got, err := ws.LexicalRel(in)
	if err != nil {
		t.Fatalf("LexicalRel(%q): %v", in, err)
	}
	if want := filepath.ToSlash(filepath.Join("protected", "x")); got != want {
		t.Fatalf("LexicalRel(%q) = %q, want %q", in, got, want)
	}
}

// TestLexicalRelViaSymlinkAlias opens a workspace through a symlink alias of
// the real root: Abs must be the symlink-evaluated real path while LexicalAbs
// keeps the caller's spelling, and an absolute path spelled through the alias
// must still map to the same relative name as the direct spelling.
func TestLexicalRelViaSymlinkAlias(t *testing.T) {
	t.Run("alias spelling", func(t *testing.T) {
		real := t.TempDir()
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(real, alias); err != nil {
			t.Skipf("create directory link: %v", err)
		}
		ws, err := Open(alias)
		if err != nil {
			t.Fatal(err)
		}
		evalReal, err := filepath.EvalSymlinks(real)
		if err != nil {
			t.Fatal(err)
		}
		if ws.Abs != evalReal {
			t.Fatalf("Abs = %q, want %q", ws.Abs, evalReal)
		}
		if ws.LexicalAbs != alias {
			t.Fatalf("LexicalAbs = %q, want %q", ws.LexicalAbs, alias)
		}
		in := filepath.Join(alias, "protected", "x")
		got, err := ws.LexicalRel(in)
		if err != nil {
			t.Fatalf("LexicalRel(%q): %v", in, err)
		}
		if want := filepath.ToSlash(filepath.Join("protected", "x")); got != want {
			t.Fatalf("LexicalRel(%q) = %q, want %q", in, got, want)
		}
	})
}

// TestLexicalRelErrors pins the fail-closed error cases: an absolute path
// outside the root, an empty path, and a nil receiver.
func TestLexicalRelErrors(t *testing.T) {
	ws := mustOpen(t)

	outside := filepath.Join(t.TempDir(), "x")
	if _, err := ws.LexicalRel(outside); err == nil {
		t.Fatalf("LexicalRel(%q) = nil error, want error for path outside root", outside)
	}
	if _, err := ws.LexicalRel(""); err == nil {
		t.Fatal("LexicalRel(\"\") = nil error, want error for empty path")
	}
	var nilRoot *Root
	if _, err := nilRoot.LexicalRel("a"); err == nil {
		t.Fatal("nil receiver LexicalRel = nil error, want error")
	}
}

// TestLexicalRelRootItself maps the lexical root itself to ".".
func TestLexicalRelRootItself(t *testing.T) {
	ws := mustOpen(t)
	got, err := ws.LexicalRel(ws.LexicalAbs)
	if err != nil {
		t.Fatalf("LexicalRel(LexicalAbs): %v", err)
	}
	if got != "." {
		t.Fatalf("LexicalRel(LexicalAbs) = %q, want %q", got, ".")
	}
}
