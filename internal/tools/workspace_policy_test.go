package tools

import "testing"

func TestIsWriteDeniedPathCaseInsensitive(t *testing.T) {
	deny := []string{".mivia/workflows", ".git"}
	for _, path := range []string{
		".MIVIA/WORKFLOWS/x.toml",
		".mivia/Workflows/x.toml",
		".GIT/config",
		".git/CONFIG",
	} {
		if !isWriteDeniedPath(path, deny) {
			t.Fatalf("isWriteDeniedPath(%q) = false, want true", path)
		}
	}
}

func TestIsWriteDeniedPathPrefixAndClean(t *testing.T) {
	deny := []string{"go.mod", "a/b", ".git"}
	cases := []struct {
		path string
		want bool
	}{
		{"go.mod", true},
		{"go.mod/", true},            // input trailing slash is cleaned
		{"a/b/c", true},              // directory prefix match
		{"a/b", true},                // exact match
		{"a/bc", false},              // no prefix boundary
		{".gitconfig", false},        // no prefix boundary
		{"sub/../.git/config", true}, // input cleans to .git/config
		{"sub/.git/config", false},   // ".git" protects the workspace root only
		{"go.mod.sum", false},
	}
	for _, tc := range cases {
		if got := isWriteDeniedPath(tc.path, deny); got != tc.want {
			t.Fatalf("isWriteDeniedPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsWriteDeniedPathSkipsDotEntries(t *testing.T) {
	// Entries that clean to "." must not block every path.
	if isWriteDeniedPath(".mivia/x", []string{".", ""}) {
		t.Fatal("dot and empty entries must not block")
	}
}
