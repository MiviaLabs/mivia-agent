package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGlobMatchDoublestarPathologicalTerminates: a pattern with many **/
// segments against a deep path is exponential for the naive recursive matcher
// (O(n^k) for k **/ segments) and would hang an agent turn. The memoized
// matcher returns instantly. The wall-clock guard is generous: the fixed
// matcher completes in microseconds, while the naive one explores on the
// order of C(25,8) ≈ 1M suffix combinations before it can conclude "no
// match", which the suite timeout catches as a hang.
func TestGlobMatchDoublestarPathologicalTerminates(t *testing.T) {
	pattern := strings.Repeat("**/", 8) + "*.go"
	path := strings.Repeat("a/", 16) + "x.go" // 17 components
	start := time.Now()
	if !globMatches(pattern, path, filepath.Base(path)) {
		t.Fatalf("pathological doublestar pattern must match %q", path)
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("positive match took %v, want < 2s", elapsed)
	}

	// Negative path: the same pattern against a non-matching extension must
	// exhaust the whole search space before returning false.
	start = time.Now()
	if globMatches(pattern, strings.Repeat("a/", 16)+"x.txt", "x.txt") {
		t.Fatal("expected non-matching extension to be rejected")
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("negative match took %v, want < 2s", elapsed)
	}
}

// TestGlobMatchesCtxCancelled pins the cancellable half of the doublestar
// invariant: a cancelled context fails the match, and the search walk's
// existing per-entry check converts that into ctx.Err() propagation.
func TestGlobMatchesCtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if globMatchesCtx(ctx, "**/*.go", "src/pkg/a.go", "a.go") {
		t.Fatal("a cancelled context must fail the doublestar match")
	}
	if globMatchesCtx(ctx, "**/pkg/**/*.go", "x/pkg/nested/b.go", "b.go") {
		t.Fatal("a cancelled context must fail the multi-doublestar match")
	}
	// A nil ctx behaves like context.Background(): matching still works.
	if !globMatchesCtx(nil, "**/*.go", "src/pkg/a.go", "a.go") {
		t.Fatal("nil ctx must behave like background and match")
	}
}

// globMatchSegmentsReference is a capped copy of the pre-fix recursive
// globMatchSegments, kept as the differential oracle for the memoized
// rewrite. The fuzz target bounds its inputs so this exponential recursion
// terminates instantly; on every shape the rewrite changed nothing about the
// result, only the cost.
func globMatchSegmentsReference(segments []string, path string, match func(string, string) bool) bool {
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
		if globMatchSegmentsReference(rest, path, match) {
			return true
		}
		for {
			idx := strings.Index(path, "/")
			if idx < 0 {
				break
			}
			path = path[idx+1:]
			if globMatchSegmentsReference(rest, path, match) {
				return true
			}
		}
		return false
	}

	// Non-empty prefix: must be consumed from the path.
	// Try literal prefix match first.
	if strings.HasPrefix(path, prefix) {
		remaining := path[len(prefix):]
		if globMatchSegmentsReference(rest, remaining, match) {
			return true
		}
		for {
			idx := strings.Index(remaining, "/")
			if idx < 0 {
				break
			}
			remaining = remaining[idx+1:]
			if globMatchSegmentsReference(rest, remaining, match) {
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
		if globMatchSegmentsReference(rest, remaining, match) {
			return true
		}
		for {
			idx := strings.Index(remaining, "/")
			if idx < 0 {
				break
			}
			remaining = remaining[idx+1:]
			if globMatchSegmentsReference(rest, remaining, match) {
				return true
			}
		}
	}
	return false
}

// FuzzGlobMatchSegmentsDifferential guards the memoized globMatchSegments
// rewrite against semantic drift: every generated input is compared against
// the capped pre-fix recursive reference. Inputs are bounded to at most 3
// segments (2 **/ hops) and 6 path components so the exponential reference
// oracle terminates instantly; the table tests cover shapes this fuzz cannot
// reach cheaply.
func FuzzGlobMatchSegmentsDifferential(f *testing.F) {
	f.Add("**/*.go", "src/pkg/a.go")
	f.Add("**/pkg/**/*.go", "x/pkg/nested/b.go")
	f.Add("src/**/internal/**/*.go", "src/pkg/internal/util.go")
	f.Add("**/foo/bar.md", "a/foo/bar.md")
	f.Add("*.md", "README.md")
	f.Add("s*rc/", "src/main.go")
	f.Fuzz(func(t *testing.T, glob, path string) {
		segments := strings.Split(glob, "**/")
		if len(segments) > 3 || len(segments) == 0 {
			return
		}
		if strings.Count(path, "/") > 5 || len(path) > 64 || len(glob) > 64 {
			return
		}
		got := globMatchSegments(segments, path, globSegmentsMatch)
		want := globMatchSegmentsReference(segments, path, globSegmentsMatch)
		if got != want {
			t.Fatalf("globMatchSegments(%q, %q) diverges from reference: memoized=%v reference=%v", segments, path, got, want)
		}
	})
}
