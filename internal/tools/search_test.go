package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestGlobMatchesMultiDoublestar(t *testing.T) {
	cases := []struct {
		glob string
		rel  string
		base string
		want bool
	}{
		// Single **/ — already works
		{"**/*.md", "README.md", "README.md", true},
		{"**/*.md", "docs/guide.md", "guide.md", true},
		{"**/*.md", "docs/deep/notes.md", "notes.md", true},

		// Multiple **/ — currently broken (only first **/ is honored)
		{"**/pkg/**/*.go", "pkg/a.go", "a.go", true},
		{"**/pkg/**/*.go", "pkg/nested/b.go", "b.go", true},
		{"**/pkg/**/*.go", "src/main.go", "main.go", false},

		// **/ with slash-containing tail — currently broken
		{"**/foo/bar.md", "foo/bar.md", "bar.md", true},
		{"**/foo/bar.md", "a/foo/bar.md", "bar.md", true},
		{"**/foo/bar.md", "a/b/foo/bar.md", "bar.md", true},
		{"**/foo/bar.md", "foo/baz.md", "baz.md", false},

		// Complex: prefix + **/ + middle + **/ + tail
		{"src/**/internal/**/*.go", "src/internal/util.go", "util.go", true},
		{"src/**/internal/**/*.go", "src/pkg/internal/util.go", "util.go", true},
		{"src/**/internal/**/*.go", "src/pkg/cmd/main.go", "main.go", false},

		// Case-insensitive glob matching
		{"**/*.MD", "README.md", "README.md", true},
		{"**/*.MD", "docs/guide.md", "guide.md", true},

		// Plain patterns (no **) — must still work
		{"*.md", "README.md", "README.md", true},
		{"*.md", "docs/guide.md", "guide.md", true},
		{"src/*.go", "src/main.go", "main.go", true},
		{"src/*.go", "pkg/main.go", "main.go", false},

		// **/ alone
		{"**/*.txt", "a.txt", "a.txt", true},
		{"**/*.txt", "x/y/z/a.txt", "a.txt", true},
	}

	for _, tc := range cases {
		got := globMatches(tc.glob, tc.rel, tc.base)
		if got != tc.want {
			t.Errorf("globMatches(%q, %q, %q) = %v, want %v",
				tc.glob, tc.rel, tc.base, got, tc.want)
		}
	}
}

func TestGrepErrorReporting(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := ws.Abs

	// Create a readable file with a match.
	if err := os.WriteFile(filepath.Join(root, "good.txt"), []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create an unreadable file with a match.
	badFile := filepath.Join(root, "bad.txt")
	if err := os.WriteFile(badFile, []byte("needle here too\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(badFile, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(badFile, 0o644)

	re := regexp.MustCompile("needle")
	matches, errs, walkErr := walkGrep(nil, ws, root, re, grepInput{}, 0, 0, nil, nil, ignoreView{})
	if walkErr != nil {
		t.Fatalf("unexpected walk error: %v", walkErr)
	}

	// Should find the match in good.txt
	found := false
	for _, m := range matches {
		if m == "good.txt:1:needle here" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected to find match in good.txt, got: %v", matches)
	}

	// Error reporting: walkErrors should have collected the permission error.
	if errs.count() == 0 {
		t.Error("expected error collection for unreadable file, got 0 errors")
	}
	if errs.notice() == "" {
		t.Error("expected non-empty error notice")
	}
	if !strings.Contains(errs.notice(), "permission denied") {
		t.Errorf("expected notice to include the error details, got: %q", errs.notice())
	}
}

func TestGrepFilesWithMatches(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := ws.Abs

	if err := os.WriteFile(filepath.Join(root, "multi.txt"),
		[]byte("line one needle\nline two needle\nline three needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "single.txt"),
		[]byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	re := regexp.MustCompile("needle")
	in := grepInput{FilesWithMatches: true}
	matches, _, err := walkGrep(nil, ws, root, re, in, 0, 0, nil, nil, ignoreView{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have exactly 2 entries (one per file), not 4 (one per line)
	if len(matches) != 2 {
		t.Errorf("files_with_matches: got %d entries, want 2: %v", len(matches), matches)
	}

	// Each entry should be a bare path, not path:line:text
	for _, m := range matches {
		if filepath.Base(m) != m {
			t.Errorf("files_with_matches: entry should be bare path, got %q", m)
		}
	}
}

func TestGrepCaseInsensitive(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := ws.Abs

	if err := os.WriteFile(filepath.Join(root, "case.txt"),
		[]byte("Hello World\nHELLO again\nlowercase hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	re := regexp.MustCompile("(?i)hello") // pre-compiled with flag
	in := grepInput{CaseInsensitive: true}
	matches, _, err := walkGrep(nil, ws, root, re, in, 0, 0, nil, nil, ignoreView{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(matches) != 3 {
		t.Errorf("case_insensitive: got %d matches, want 3: %v", len(matches), matches)
	}
}

func TestGrepPaginationOffsetLimit(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := ws.Abs

	var lines string
	for i := 0; i < 20; i++ {
		lines += "needle\n"
	}
	if err := os.WriteFile(filepath.Join(root, "many.txt"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pagination is applied in executeGrep, not walkGrep.
	// Test via the grepTool.Execute path.
	reg := NewRegistry()
	reg.Register(&grepTool{ws: ws, maxMatches: 0, maxBytes: 0, ignore: nil})
	ctx := context.Background()

	// Page 1: offset=0, limit=5
	out1, err := reg.Execute(ctx, "grep", json.RawMessage(`{"pattern":"needle","offset":0,"limit":5}`))
	if err != nil {
		t.Fatalf("page 1 error: %v", err)
	}
	count1 := strings.Count(out1, "needle")
	if count1 != 5 {
		t.Errorf("page 1: got %d matches, want 5: %s", count1, out1)
	}
	if !strings.Contains(out1, "more matches") {
		t.Errorf("page 1: expected 'more matches' trailer, got: %s", out1)
	}

	// Page 2: offset=5, limit=5
	out2, err := reg.Execute(ctx, "grep", json.RawMessage(`{"pattern":"needle","offset":5,"limit":5}`))
	if err != nil {
		t.Fatalf("page 2 error: %v", err)
	}
	count2 := strings.Count(out2, "needle")
	if count2 != 5 {
		t.Errorf("page 2: got %d matches, want 5: %s", count2, out2)
	}

	// Pages should not overlap
	if strings.Contains(out1, "many.txt:1:") && strings.Contains(out2, "many.txt:1:") {
		t.Errorf("pages overlap: page1 and page2 both contain line 1")
	}
}

func TestGlobPathRoot(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := ws.Abs

	for _, p := range []string{
		"docs/README.md",
		"docs/guide.md",
		"src/main.go",
		"src/util.go",
	} {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Verify the matching logic for a scoped walk.
	docsRoot, err := ws.Resolve("docs")
	if err != nil {
		t.Fatal(err)
	}

	var hits []string
	filepath.WalkDir(docsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel := ws.Rel(path)
		if globMatches("*.md", rel, d.Name()) {
			hits = append(hits, rel)
		}
		return nil
	})

	for _, h := range hits {
		switch filepath.Base(h) {
		case "main.go", "util.go":
			t.Errorf("glob path root: found %q which is not under docs/", h)
		}
	}
	foundReadme, foundGuide := false, false
	for _, h := range hits {
		switch filepath.Base(h) {
		case "README.md":
			foundReadme = true
		case "guide.md":
			foundGuide = true
		}
	}
	if !foundReadme || !foundGuide {
		t.Errorf("glob path root: missing expected files, got %v", hits)
	}
}

// globSegmentsMatch mirrors the matcher closure used inside globMatches so the
// globMatchSegments unit tests exercise the exact same semantics (plain
// filepath.Match plus the case-insensitive retry).
func globSegmentsMatch(pattern, name string) bool {
	if ok, err := filepath.Match(pattern, name); err == nil && ok {
		return true
	}
	ok, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(name))
	return err == nil && ok
}

// TestGlobMatchSegmentsSingleSegment covers the len(segments)==1 branch: the
// last segment is matched against the base name and, failing that, the full
// remaining path.
func TestGlobMatchSegmentsSingleSegment(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// Base-name match.
		{"*.go", "main.go", true},
		{"*.go", "src/main.go", true},
		{"main.go", "src/main.go", true},
		// Full-path match: the base fails but the whole path matches.
		{"src/*.go", "src/main.go", true},
		{"src/main.go", "src/main.go", true},
		// No match.
		{"*.go", "main.txt", false},
		{"*.txt", "src/main.go", false},
		{"src/*.go", "pkg/main.go", false},
		{"main.go", "src/main.txt", false},
	}
	for _, tc := range cases {
		got := globMatchSegments([]string{tc.pattern}, tc.path, globSegmentsMatch)
		if got != tc.want {
			t.Errorf("globMatchSegments([%q], %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

// TestGlobMatchSegmentsLeadingDoublestar covers the leading "" prefix branch:
// "**/*.go" splits into ["", "*.go"] and must match at any directory depth.
func TestGlobMatchSegmentsLeadingDoublestar(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"a.go", true},
		{"src/a.go", true},
		{"src/pkg/a.go", true},
		{"src/pkg/nested/a.go", true},
		{"a.txt", false},
		{"src/a.txt", false},
	}
	for _, tc := range cases {
		got := globMatchSegments([]string{"", "*.go"}, tc.path, globSegmentsMatch)
		if got != tc.want {
			t.Errorf("globMatchSegments([\"\", \"*.go\"], %q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestGlobMatchSegmentsLiteralPrefix covers the non-empty prefix consumed as a
// literal string: "src/**/*.go" splits into ["src/", "*.go"] and "src/" must
// be a literal prefix of the path.
func TestGlobMatchSegmentsLiteralPrefix(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"src/a.go", true},
		{"src/pkg/b.go", true},
		{"src/pkg/nested/b.go", true},
		{"lib/a.go", false},
		{"src/a.txt", false},
	}
	for _, tc := range cases {
		got := globMatchSegments([]string{"src/", "*.go"}, tc.path, globSegmentsMatch)
		if got != tc.want {
			t.Errorf("globMatchSegments([\"src/\", \"*.go\"], %q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestGlobMatchSegmentsGlobPrefix covers the fallback branch where the prefix
// is itself a glob matched against the first path segment: "s*rc/**/*.go"
// splits into ["s*rc/", "*.go"].
func TestGlobMatchSegmentsGlobPrefix(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"src/main.go", true},
		{"src/pkg/main.go", true},
		{"serc/main.go", true},
		{"a/src/main.go", false},
		{"src/main.txt", false},
	}
	for _, tc := range cases {
		got := globMatchSegments([]string{"s*rc/", "*.go"}, tc.path, globSegmentsMatch)
		if got != tc.want {
			t.Errorf("globMatchSegments([\"s*rc/\", \"*.go\"], %q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIgnoreDir(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		want     bool
	}{
		{".git", []string{".git", "node_modules", "vendor"}, true},
		{"node_modules", []string{".git", "node_modules", "vendor"}, true},
		{"vendor", []string{".git", "node_modules", "vendor"}, true},
		{"src", []string{".git", "node_modules", "vendor"}, false},
		{"target", []string{".git", "target"}, true},
		{"__pycache__", []string{"__pycache__", ".venv"}, true},
		{"docs", []string{}, false},
	}
	for _, tc := range cases {
		got := ignoreDir(tc.name, tc.patterns)
		if got != tc.want {
			t.Errorf("ignoreDir(%q, %v) = %v, want %v", tc.name, tc.patterns, got, tc.want)
		}
	}
}

func TestWalkErrors(t *testing.T) {
	we := &walkErrors{maxErrs: 3}
	we.add("a.txt", os.ErrPermission)
	we.add("b.txt", os.ErrPermission)
	we.add("c.txt", os.ErrPermission)
	we.add("d.txt", os.ErrPermission) // capped
	if we.count() != 3 {
		t.Errorf("expected 3 errors, got %d", we.count())
	}
	notice := we.notice()
	want := "\n... 3 files skipped (first: a.txt: permission denied)"
	if notice != want {
		t.Errorf("notice() = %q, want %q", notice, want)
	}

	// A single error uses the singular form with full details.
	we1 := &walkErrors{maxErrs: 3}
	we1.add("a.txt", os.ErrPermission)
	if got, want := we1.notice(), "\n... 1 file skipped: a.txt: permission denied"; got != want {
		t.Errorf("notice() = %q, want %q", got, want)
	}

	we0 := &walkErrors{maxErrs: 0}
	we0.add("a.txt", os.ErrPermission)
	if we0.count() != 0 {
		t.Errorf("maxErrs=0 should collect nothing, got %d", we0.count())
	}
	if notice := we0.notice(); notice != "" {
		t.Errorf("maxErrs=0 should produce empty notice, got %q", notice)
	}
}
