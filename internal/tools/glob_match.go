package tools

import (
	"context"
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
	return globMatchesCtx(context.Background(), glob, rel, base)
}

// globMatchesCtx is globMatches with cancellation: per-file doublestar
// matching honors ctx.Done() so a cancelled search walk stops matching files
// instead of grinding through a pathological **/ pattern. A cancelled context
// fails the match - walkFilteredFiles' per-entry check converts that into
// ctx.Err() - and a nil ctx behaves like context.Background().
func globMatchesCtx(ctx context.Context, glob, rel, base string) bool {
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
	return globMatchSegmentsCtx(ctx, parts, rel, match)
}

// globMatchSegments recursively matches a list of glob segments against a path.
// Each segment except the last must end with "/" (directory-level match).
// A **/ segment matches zero or more path components.
func globMatchSegments(segments []string, path string, match func(string, string) bool) bool {
	return globMatchSegmentsCtx(context.Background(), segments, path, match)
}

// globMatchKey memoizes one (segment index, path suffix) pair. The suffix is
// identified by its byte offset into the original path, which is unique
// because every recursion below cuts path at a component boundary.
type globMatchKey struct {
	segIdx int
	offset int
}

// globMatchSegmentsCtx is the cancellable, memoized recursion behind
// globMatchSegments. Results are cached per (segment index, path suffix), so
// the worst case for k **/ segments over c path components drops from the
// exponential O(c^k) of a naive recursion to O(len(segments) × c²), and the
// memo holds at most len(segments) × (c+1) entries. ctx.Done() is honored at
// every recursion entry - a cancelled context fails the match (callers
// convert that into ctx.Err()) - and a nil ctx behaves like
// context.Background(). Branch order and first-success short-circuit are
// identical to the original recursion, so results are unchanged.
func globMatchSegmentsCtx(ctx context.Context, segments []string, path string, match func(string, string) bool) bool {
	if len(segments) == 0 {
		return false
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return false
		default:
		}
	}
	memo := make(map[globMatchKey]bool)
	return globMatchSegmentsRec(ctx, segments, 0, path, len(path), match, memo)
}

// globMatchSegmentsRec is the memoized recursion of globMatchSegmentsCtx.
// origLen is the byte length of the original path; a suffix's offset is
// origLen - len(path), so the key identifies exactly one suffix.
func globMatchSegmentsRec(ctx context.Context, segments []string, segIdx int, path string, origLen int, match func(string, string) bool, memo map[globMatchKey]bool) bool {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return false
		default:
		}
	}
	key := globMatchKey{segIdx: segIdx, offset: origLen - len(path)}
	if v, ok := memo[key]; ok {
		return v
	}
	v := globMatchSegmentsStep(ctx, segments, segIdx, path, origLen, match, memo)
	memo[key] = v
	return v
}

// globMatchSegmentsStep performs one level of segment matching. It mirrors the
// original branch order exactly: last segment first, then the leading **/
// zero-or-more-directories branch, then literal-prefix consumption, then the
// glob-prefix fallback, each short-circuiting on the first success.
func globMatchSegmentsStep(ctx context.Context, segments []string, segIdx int, path string, origLen int, match func(string, string) bool, memo map[globMatchKey]bool) bool {
	if segIdx == len(segments)-1 {
		// Last segment: match against the full remaining path or just base.
		pat := segments[segIdx]
		base := filepath.Base(path)
		return match(pat, base) || match(pat, path)
	}

	prefix := segments[segIdx]
	next := segIdx + 1

	if prefix == "" {
		// Leading **/: can match zero or more directories.
		if globMatchSegmentsRec(ctx, segments, next, path, origLen, match, memo) {
			return true
		}
		for {
			idx := strings.Index(path, "/")
			if idx < 0 {
				break
			}
			path = path[idx+1:]
			if globMatchSegmentsRec(ctx, segments, next, path, origLen, match, memo) {
				return true
			}
		}
		return false
	}

	// Non-empty prefix: must be consumed from the path.
	// Try literal prefix match first.
	if strings.HasPrefix(path, prefix) {
		remaining := path[len(prefix):]
		if globMatchSegmentsRec(ctx, segments, next, remaining, origLen, match, memo) {
			return true
		}
		for {
			idx := strings.Index(remaining, "/")
			if idx < 0 {
				break
			}
			remaining = remaining[idx+1:]
			if globMatchSegmentsRec(ctx, segments, next, remaining, origLen, match, memo) {
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
		if globMatchSegmentsRec(ctx, segments, next, remaining, origLen, match, memo) {
			return true
		}
		for {
			idx := strings.Index(remaining, "/")
			if idx < 0 {
				break
			}
			remaining = remaining[idx+1:]
			if globMatchSegmentsRec(ctx, segments, next, remaining, origLen, match, memo) {
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
// The first error is remembered separately so the notice can report what kind
// of error caused the skips (e.g. "permission denied").
type walkErrors struct {
	errs     []string
	maxErrs  int
	firstErr string
}

func (we *walkErrors) add(path string, err error) {
	if we.maxErrs <= 0 {
		return
	}
	if len(we.errs) >= we.maxErrs {
		return
	}
	msg := fmt.Sprintf("%s: %s", path, err)
	if len(we.errs) == 0 {
		we.firstErr = msg
	}
	we.errs = append(we.errs, msg)
}

func (we *walkErrors) count() int { return len(we.errs) }

func (we *walkErrors) notice() string {
	if we.count() == 0 {
		return ""
	}
	if we.count() == 1 {
		return fmt.Sprintf("\n... 1 file skipped: %s", we.firstErr)
	}
	return fmt.Sprintf("\n... %d files skipped (first: %s)", we.count(), we.firstErr)
}
