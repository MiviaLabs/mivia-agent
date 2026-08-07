package tools

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	ignore "git.sr.ht/~jamesponddotco/gitignore-go"
)

// gitignoreStamp is the staleness key for the root .gitignore: mtime + size +
// content hash. Hashing closes the same-second same-size rewrite hole that
// mtime+size alone miss.
type gitignoreStamp struct {
	exists bool
	mtime  time.Time
	size   int64
	hash   [sha256.Size]byte
}

// gitignoreMatcher is the registry-owned ignore decision source: built-in and
// configured name patterns plus a reloadable root .gitignore. Nested
// .gitignore files are not loaded. Callers capture an immutable ignoreView via
// snapshot() at walk entry so a concurrent reload cannot tear a mid-walk view.
//
// A matcher with root="" is inert for gitignore rules (patterns still apply).
type gitignoreMatcher struct {
	root     string
	patterns []string // floor + search_ignore_patterns; immutable after construction

	mu    sync.Mutex
	m     *ignore.File
	stamp gitignoreStamp
}

// newGitignoreMatcher creates a matcher for the given workspace root with no
// name-pattern floor. Prefer newIgnoreSource when composing the full decision.
func newGitignoreMatcher(root string) *gitignoreMatcher {
	return &gitignoreMatcher{root: root}
}

// newIgnoreSource creates the shared ignore decision for list_dir/grep/glob.
// patterns is copied; root may be empty (gitignore inert, patterns still apply).
func newIgnoreSource(root string, patterns []string) *gitignoreMatcher {
	return &gitignoreMatcher{
		root:     root,
		patterns: append([]string(nil), patterns...),
	}
}

// ignoreView is an immutable snapshot of the ignore decision for one walk.
// The compiled *ignore.File is never mutated after creation; patterns are not
// modified after matcher construction.
type ignoreView struct {
	root     string
	patterns []string
	m        *ignore.File
}

// snapshot returns a coherent ignoreView. Under the mutex it stats/reads the
// root .gitignore, reloads when the stamp changes, then returns the compiled
// rules plus the name-pattern list.
func (g *gitignoreMatcher) snapshot() ignoreView {
	if g == nil {
		return ignoreView{}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reloadLocked()
	return ignoreView{
		root:     g.root,
		patterns: g.patterns,
		m:        g.m,
	}
}

// ShouldIgnoreDir reports whether a directory should be collapsed and not
// descended: built-in/config name patterns OR root-gitignore directory rules.
func (v ignoreView) ShouldIgnoreDir(name, rel string) bool {
	if ignoreDir(name, v.patterns) {
		return true
	}
	if v.m == nil || v.root == "" {
		return false
	}
	// gitignore-go patterns are relative to the workspace root (anchored or
	// any-depth); matching root-joined absolute paths would collapse every path
	// when the root itself sits inside an ignored prefix (e.g. a worktree at
	// <repo>/.mivia/worktrees/wt with a ".mivia/worktrees/" rule). Match the
	// workspace-relative path instead.
	return v.m.Match(filepath.ToSlash(rel) + "/")
}

// ShouldIgnoreFile reports whether a file path matches root-gitignore rules.
// Name patterns apply only to directories (floor is directory-name based).
func (v ignoreView) ShouldIgnoreFile(name, rel string) bool {
	_ = name
	if v.m == nil || v.root == "" {
		return false
	}
	// Match the workspace-relative path (see ShouldIgnoreDir).
	return v.m.Match(rel)
}

// Match reports whether absPath should be excluded according to .gitignore
// rules. An inert or failed matcher returns false. Reloads if the stamp changed.
func (g *gitignoreMatcher) Match(absPath string) bool {
	if g == nil || g.root == "" {
		return false
	}
	v := g.snapshot()
	if v.m == nil {
		return false
	}
	return v.m.Match(absPath)
}

// MatchRel reports whether relPath (workspace-relative) should be excluded.
func (g *gitignoreMatcher) MatchRel(relPath string) bool {
	if g == nil || g.root == "" {
		return false
	}
	v := g.snapshot()
	if v.m == nil {
		return false
	}
	// Match the workspace-relative path, not the root-joined absolute path
	// (see ShouldIgnoreDir).
	return v.m.Match(relPath)
}

// IsDir returns true if the directory at relPath matches a directory-only
// .gitignore pattern (e.g., "build/" or "**/dist/").
func (g *gitignoreMatcher) IsDir(relPath string) bool {
	if g == nil || g.root == "" {
		return false
	}
	v := g.snapshot()
	if v.m == nil {
		return false
	}
	// Match the workspace-relative path with a trailing slash for the
	// directory-only pattern form (see ShouldIgnoreDir).
	return v.m.Match(filepath.ToSlash(relPath) + "/")
}

// Patterns returns the name-pattern floor (for tests and diagnostics).
func (g *gitignoreMatcher) Patterns() []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.patterns...)
}

func (g *gitignoreMatcher) reloadLocked() {
	if g.root == "" {
		g.m = nil
		g.stamp = gitignoreStamp{}
		return
	}
	giPath := filepath.Join(g.root, ".gitignore")
	st, err := os.Stat(giPath)
	if err != nil {
		// Missing or unreadable — normal; match nothing from gitignore.
		g.m = nil
		g.stamp = gitignoreStamp{}
		return
	}
	data, err := os.ReadFile(giPath)
	if err != nil {
		g.m = nil
		g.stamp = gitignoreStamp{}
		return
	}
	hash := sha256.Sum256(data)
	stamp := gitignoreStamp{
		exists: true,
		mtime:  st.ModTime(),
		size:   st.Size(),
		hash:   hash,
	}
	// Unchanged content (hash) — skip recompile. Still refresh mtime/size so
	// the stamp tracks the file even when only metadata moved.
	if g.stamp.exists && g.stamp.hash == stamp.hash {
		g.stamp = stamp
		return
	}
	lines := splitGitignoreLines(data)
	m, err := ignore.NewFromLines(lines)
	if err != nil {
		g.m = nil
		g.stamp = gitignoreStamp{}
		return
	}
	g.m = m
	g.stamp = stamp
}

func splitGitignoreLines(data []byte) []string {
	// NewFromLines joins with newlines; preserve empty trailing line behavior
	// by splitting the same way the file reader would scan.
	s := string(data)
	if s == "" {
		return nil
	}
	// Trim a single trailing newline so we don't invent an empty final line
	// that Parse would skip anyway — either way is fine; keep it simple.
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}
