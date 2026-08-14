package pathutil

import (
	"os"
	"testing"
)

// TestSplitExt pins the documented contract: ext is the last-dot suffix of
// the final path element, leading dot runs are not extensions, base + ext
// always reconstructs the input, and empty and separator-only inputs stay
// intact.
func TestSplitExt(t *testing.T) {
	cases := []struct {
		name string
		p    string
		base string
		ext  string
	}{
		{"empty", "", "", ""},
		{"no extension", "archive", "archive", ""},
		{"simple extension", "archive.zip", "archive", ".zip"},
		{"nested path", "a/b/c.txt", "a/b/c", ".txt"},
		{"last dot wins", "archive.tar.gz", "archive.tar", ".gz"},
		{"dot in directory", "a.dir/b", "a.dir/b", ""},
		{"dot in directory with extension", "a.dir/b.go", "a.dir/b", ".go"},
		{"trailing separator", "a.dir/", "a.dir/", ""},
		{"trailing dot", "archive.", "archive", "."},
		{"hidden file", ".env", ".env", ""},
		{"hidden file with extension", ".env.local", ".env", ".local"},
		{"hidden file in directory", "a/b/.hidden", "a/b/.hidden", ""},
		{"current directory", ".", ".", ""},
		{"parent directory", "..", "..", ""},
		{"all dots", "...", "...", ""},
		{"non-ascii base", "文件.txt", "文件", ".txt"},
		{"absolute path", "/etc/hosts", "/etc/hosts", ""},
		{"absolute path with extension", "/etc/host.conf", "/etc/host", ".conf"},
	}
	for _, c := range cases {
		base, ext := SplitExt(c.p)
		if base != c.base || ext != c.ext {
			t.Errorf("SplitExt(%q) = (%q, %q), want (%q, %q)", c.p, base, ext, c.base, c.ext)
		}
		if base+ext != c.p {
			t.Errorf("SplitExt(%q): base+ext = %q, want the original path back", c.p, base+ext)
		}
	}
}

// FuzzSplitExt checks the SplitExt invariants over arbitrary input: base + ext
// always reconstructs p, ext never contains a path separator, and a non-empty
// ext always begins with a dot.
func FuzzSplitExt(f *testing.F) {
	for _, seed := range []string{"", "archive.zip", "a/b/c.txt", ".env", "a.dir/b"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, p string) {
		base, ext := SplitExt(p)
		if base+ext != p {
			t.Fatalf("SplitExt(%q) = (%q, %q): base+ext != p", p, base, ext)
		}
		for i := 0; i < len(ext); i++ {
			if os.IsPathSeparator(ext[i]) {
				t.Fatalf("SplitExt(%q): ext %q contains a path separator", p, ext)
			}
		}
		if ext != "" && ext[0] != '.' {
			t.Fatalf("SplitExt(%q): ext %q does not begin with a dot", p, ext)
		}
	})
}
