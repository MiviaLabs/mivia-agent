package pathutil

import (
	"strings"
	"testing"
)

// TestSplitExt covers empty input, names with and without extensions, nested
// paths, multi-part extensions, hidden files, trailing dots, separator-only
// tails, and both "/" and "\\" separators.
func TestSplitExt(t *testing.T) {
	cases := []struct {
		name string
		path string
		base string
		ext  string
	}{
		{"empty", "", "", ""},
		{"no extension", "file", "file", ""},
		{"single extension", "file.txt", "file", ".txt"},
		{"nested path", "dir/file.txt", "dir/file", ".txt"},
		{"deep path without extension", "dir/sub/file", "dir/sub/file", ""},
		{"multi-part extension keeps inner", "archive.tar.gz", "archive.tar", ".gz"},
		{"hidden file has no extension", ".bashrc", ".bashrc", ""},
		{"hidden file in directory", "dir/.bashrc", "dir/.bashrc", ""},
		{"hidden file at root", "/.bashrc", "/.bashrc", ""},
		{"trailing dot is an extension", "file.", "file", "."},
		{"dot in earlier element only", "a.b/c", "a.b/c", ""},
		{"backslash separator", `a.b\c.d`, `a.b\c`, ".d"},
		{"backslash hidden file", `dir\.bashrc`, `dir\.bashrc`, ""},
		{"trailing separator", "dir/file/", "dir/file/", ""},
		{"parent directory reference", "..", ".", "."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, ext := SplitExt(c.path)
			if base != c.base || ext != c.ext {
				t.Errorf("SplitExt(%q) = (%q, %q), want (%q, %q)", c.path, base, ext, c.base, c.ext)
			}
			if base+ext != c.path {
				t.Errorf("SplitExt(%q) does not round-trip: base+ext = %q", c.path, base+ext)
			}
		})
	}
}

// TestSplitExtLongPath checks the oversized input: a very long path splits
// without panicking and still round-trips.
func TestSplitExtLongPath(t *testing.T) {
	long := strings.Repeat("segment/", 1000) + strings.Repeat("a", 4096) + ".txt"
	base, ext := SplitExt(long)
	if ext != ".txt" {
		t.Errorf("SplitExt(long path) ext = %q, want %q", ext, ".txt")
	}
	if want := strings.TrimSuffix(long, ".txt"); base != want {
		t.Errorf("SplitExt(long path) base does not strip the extension")
	}
	if base+ext != long {
		t.Errorf("SplitExt(long path) does not round-trip")
	}
}
