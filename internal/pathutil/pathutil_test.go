package pathutil

import (
	"strings"
	"testing"
)

// TestSplitExt covers empty input, simple extensions, trailing dots, hidden
// files, multiple dots, separators, trailing separators, Windows-style paths,
// and dot and double-dot elements.
func TestSplitExt(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantBase string
		wantExt  string
	}{
		{"empty", "", "", ""},
		{"no dot", "file", "file", ""},
		{"simple extension", "file.txt", "file", ".txt"},
		{"trailing dot", "file.", "file", "."},
		{"hidden file", ".gitignore", ".gitignore", ""},
		{"hidden file in dir", "dir/.env", "dir/.env", ""},
		{"last dot wins", "archive.tar.gz", "archive.tar", ".gz"},
		{"slash separated", "dir/file.txt", "dir/file", ".txt"},
		{"dot in earlier element", "a.b/c", "a.b/c", ""},
		{"trailing separator", "a/b/", "a/b/", ""},
		{"windows backslashes", `C:\data\file.txt`, `C:\data\file`, ".txt"},
		{"dot-only element", ".", ".", ""},
		{"double dot", "..", ".", "."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base, ext := SplitExt(c.in)
			if base != c.wantBase || ext != c.wantExt {
				t.Errorf("SplitExt(%q) = (%q, %q), want (%q, %q)",
					c.in, base, ext, c.wantBase, c.wantExt)
			}
		})
	}
}

// FuzzSplitExt checks that base and ext always reconstruct the input and
// that an extension is a dot-prefixed suffix free of path separators.
func FuzzSplitExt(f *testing.F) {
	f.Add("")
	f.Add("file.txt")
	f.Add("dir/file.txt")
	f.Add(".gitignore")
	f.Add("archive.tar.gz")
	f.Add(`C:\data\file.txt`)
	f.Add("a.b/c")
	f.Fuzz(func(t *testing.T, p string) {
		base, ext := SplitExt(p)
		if base+ext != p {
			t.Fatalf("SplitExt(%q) = (%q, %q); base+ext = %q, want %q",
				p, base, ext, base+ext, p)
		}
		if ext != "" {
			if !strings.HasSuffix(p, ext) || ext[0] != '.' {
				t.Fatalf("SplitExt(%q) ext = %q, want dot-prefixed suffix", p, ext)
			}
			if strings.Contains(ext, "/") || strings.Contains(ext, "\\") {
				t.Fatalf("SplitExt(%q) ext = %q, want no separators", p, ext)
			}
		}
	})
}
