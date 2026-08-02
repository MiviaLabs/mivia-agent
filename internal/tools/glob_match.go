package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// globMatches reports whether a filename filter selects a file.
//
// The pattern is a glob that may contain zero or more `**/` segments meaning
// "zero or more directories". Multiple `**/` are supported: `**/pkg/**/*.go`
// matches `pkg/a.go`, `pkg/nested/b.go`, `x/pkg/y/z.go`.
//
// Matching is case-insensitive on the extension-style patterns people
// actually type (e.g. `*.MD` finds `README.md`).
//
// Accepted, in order:
//  1. A bare pattern against the base name (`"*.md"`)
//  2. The same pattern against the workspace-relative path (`"src/*.go"`)
//  3. A `**/`-aware match for patterns containing double-star segments
func globMatches(glob, rel, base string) bool {
	glob = filepath.ToSlash(glob)
	rel = filepath.ToSlash(rel)

	match := func(pattern, name string) bool {
		if ok, err := filepath.Match(pattern, name); err == nil && ok {
			return true
		}
		// Case-insensitive retry: "*.MD" should still find README.md.
		ok, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(name))
		return err == nil && ok
	}

	// Fast path: no ** at all.
	if !strings.Contains(glob, "**") {
		return match(glob, base) || match(glob, rel)
	}

	// Split on **/ segments and match using recursive segment matching.
	// "src/**/internal/**/*.go" becomes segments ["src/", "internal/", "*.go"]
	// where each "/"-terminated segment must be consumed by a directory level
	// and the last segment is matched against the remaining path.
	parts := strings.Split(glob, "**/")
	return globMatchSegments(parts, rel, match)
}

// globMatchSegments recursively matches a list of glob segments against a path.
// Each segment except the last must end with "/" (directory-level match).
// A **/ segment matches zero or more path components.
func globMatchSegments(segments []string, path string, match func(string, string) bool) bool {
	if len(segments) == 0 {
		return false
	}
	if len(segments) == 1 {
		// Last segment: match against the full remaining path or just base.
		pat := segments[0]
		base := filepath.Base(path)
		return match(pat, base) || match(pat, path)
	}

	prefix := segments[0]
	rest := segments[1:]

	if prefix == "" {
		// Leading **/: can match zero or more directories.
		if globMatchSegments(rest, path, match) {
			return true
		}
		for {
			idx := strings.Index(path, "/")
			if idx < 0 {
				break
			}
			path = path[idx+1:]
			if globMatchSegments(rest, path, match) {
				return true
			}
		}
		return false
	}

	// Non-empty prefix: must be consumed from the path.
	// Try literal prefix match first.
	if strings.HasPrefix(path, prefix) {
		remaining := path[len(prefix):]
		// **/ after prefix: try zero then one-or-more directories.
		if globMatchSegments(rest, remaining, match) {
			return true
		}
		for {
			idx := strings.Index(remaining, "/")
			if idx < 0 {
				break
			}
			remaining = remaining[idx+1:]
			if globMatchSegments(rest, remaining, match) {
				return true
			}
		}
		return false
	}

	// Try prefix as a glob against one path segment.
	idx := strings.Index(path, "/")
	var seg string
	if idx >= 0 {
		seg = path[:idx+1]
	} else {
		seg = path
	}
	if match(prefix, seg) {
		remaining := path[len(seg):]
		if globMatchSegments(rest, remaining, match) {
			return true
		}
		for {
			idx := strings.Index(remaining, "/")
			if idx < 0 {
				break
			}
			remaining = remaining[idx+1:]
			if globMatchSegments(rest, remaining, match) {
				return true
			}
		}
	}
	return false
}

// matchGlob is the legacy single-argument matcher, kept for callers that
// have only one string. Prefer globMatches, which knows both the
// workspace-relative path and the base name.
func matchGlob(pattern, name string) bool {
	return globMatches(pattern, name, filepath.Base(name))
}

// ignoreDir reports whether a directory should be skipped during a search walk.
// It checks the directory name against the configured ignore patterns.
func ignoreDir(name string, patterns []string) bool {
	for _, p := range patterns {
		if name == p {
			return true
		}
	}
	return false
}

// walkErrors collects per-file errors during a search walk, capped at maxErrs.
type walkErrors struct {
	errs    []string
	maxErrs int
}

func (we *walkErrors) add(path string, err error) {
	if we.maxErrs <= 0 {
		return
	}
	if len(we.errs) >= we.maxErrs {
		return
	}
	we.errs = append(we.errs, fmt.Sprintf("%s: %s", path, err))
}

func (we *walkErrors) count() int { return len(we.errs) }

func (we *walkErrors) notice() string {
	if we.count() == 0 {
		return ""
	}
	if we.count() == 1 {
		return "\n... 1 file skipped (errors)"
	}
	return fmt.Sprintf("\n... %d files skipped (errors)", we.count())
}
