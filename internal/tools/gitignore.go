package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	ignore "git.sr.ht/~jamesponddotco/gitignore-go"
)

// gitignoreMatcher provides lazy-loaded .gitignore matching for a workspace
// root. It loads the root .gitignore on first use. Nested .gitignore files
// in subdirectories are NOT loaded — only the root-level file is used, keeping
// the matcher stateless and concurrent-safe without a directory cache.
//
// A nil matcher (from newGitignoreMatcher with root="") matches nothing.
type gitignoreMatcher struct {
	root string
	once sync.Once
	m    *ignore.File
}

// newGitignoreMatcher creates a matcher for the given workspace root.
// If root is empty, the matcher is inert (matches nothing).
func newGitignoreMatcher(root string) *gitignoreMatcher {
	return &gitignoreMatcher{root: root}
}

// Match reports whether absPath should be excluded according to .gitignore
// rules. An inert matcher always returns false. A failed load also returns
// false — callers fall back to the built-in ignorePatterns.
func (g *gitignoreMatcher) Match(absPath string) bool {
	if g.root == "" {
		return false
	}
	g.once.Do(g.load)
	if g.m == nil {
		return false
	}
	return g.m.Match(absPath)
}

// MatchRel reports whether relPath (workspace-relative) should be excluded.
// The path is joined with the workspace root for the matcher.
func (g *gitignoreMatcher) MatchRel(relPath string) bool {
	if g.root == "" {
		return false
	}
	return g.Match(filepath.Join(g.root, relPath))
}

// IsDir returns true if the directory at relPath matches a directory-only
// .gitignore pattern (e.g., "build/" or "**/dist/"). This is used to
// short-circuit directory walks.
func (g *gitignoreMatcher) IsDir(relPath string) bool {
	if g.root == "" {
		return false
	}
	g.once.Do(g.load)
	if g.m == nil {
		return false
	}
	// Append "/" to test for directory-only patterns. The matcher normalizes
	// to forward slashes, so this works on all platforms.
	dirPath := filepath.Join(g.root, relPath) + string(os.PathSeparator)
	return g.m.Match(strings.ReplaceAll(dirPath, string(os.PathSeparator), "/"))
}

func (g *gitignoreMatcher) load() {
	if g.root == "" {
		return
	}
	giPath := filepath.Join(g.root, ".gitignore")
	m, err := ignore.New(giPath)
	if err != nil {
		// No .gitignore or unreadable — this is normal. Set m to nil so
		// Match always returns false.
		if os.IsNotExist(err) {
			return
		}
		// Unreadable .gitignore — log nothing, match nothing. The built-in
		// ignorePatterns still apply.
		return
	}
	g.m = m
}
