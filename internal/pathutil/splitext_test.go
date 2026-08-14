package pathutil

import "testing"

// TestSplitExt covers empty input, no extension, single and multi-dot
// extensions, dots in directory names, hidden-file names, trailing dots, and
// both separator styles. The base and extension always rejoin to the input:
// base + ext == in for every case.
func TestSplitExt(t *testing.T) {
	cases := []struct {
		name string
		in   string
		base string
		ext  string
	}{
		{"empty", "", "", ""},
		{"no extension", "file", "file", ""},
		{"single extension", "file.txt", "file", ".txt"},
		{"last dot wins", "archive.tar.gz", "archive.tar", ".gz"},
		{"leading dot hidden file", ".bashrc", ".bashrc", ""},
		{"hidden file with later dot", ".gitignore.go", ".gitignore", ".go"},
		{"dot in directory", "dir.with.dots/file", "dir.with.dots/file", ""},
		{"dot in directory and file", "dir.with.dots/file.txt", "dir.with.dots/file", ".txt"},
		{"hidden file in directory", "dir/.hidden", "dir/.hidden", ""},
		{"trailing dot", "file.", "file.", ""},
		{"single character", "a", "a", ""},
		{"slash separator", "dir/file.txt", "dir/file", ".txt"},
		{"backslash separator", `dir\file.txt`, `dir\file`, ".txt"},
		{"trailing separator", "dir/", "dir/", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, ext := SplitExt(c.in)
			if base != c.base || ext != c.ext {
				t.Errorf("SplitExt(%q) = (%q, %q), want (%q, %q)", c.in, base, ext, c.base, c.ext)
			}
			if base+ext != c.in {
				t.Errorf("SplitExt(%q) = (%q, %q), but base+ext != input", c.in, base, ext)
			}
		})
	}
}
