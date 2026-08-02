package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestGitignoreMatcherBasic(t *testing.T) {
	dir := t.TempDir()
	root, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create files that should and shouldn't be ignored.
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "build", "out.o"), []byte("binary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dist", "app"), []byte("app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No .gitignore → nothing is ignored.
	gi := newGitignoreMatcher(root.Abs)
	if gi.MatchRel("build/out.o") {
		t.Error("build/out.o should NOT be ignored without .gitignore")
	}

	// Write .gitignore.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\ndist/\n*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reload matcher (new instance since load is sync.Once).
	gi2 := newGitignoreMatcher(root.Abs)

	// build/ is a directory pattern — IsDir should match.
	if !gi2.IsDir("build") {
		t.Error("IsDir(build) should return true for 'build/' pattern")
	}
	if !gi2.IsDir("dist") {
		t.Error("IsDir(dist) should return true for 'dist/' pattern")
	}

	// Files in ignored directories should be ignored.
	if !gi2.MatchRel("build/out.o") {
		t.Error("build/out.o should be ignored")
	}
	if !gi2.MatchRel("dist/app") {
		t.Error("dist/app should be ignored")
	}

	// Non-ignored file should NOT be ignored.
	if gi2.MatchRel("keep.txt") {
		t.Error("keep.txt should NOT be ignored")
	}
}

func TestGitignoreGlobIntegration(t *testing.T) {
	dir := t.TempDir()
	root, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create workspace structure.
	for _, p := range []string{
		"src/main.go",
		"src/util.go",
		"build/out.o",
		"dist/app.zip",
		"node_modules/pkg/index.js",
		"README.md",
	} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Write .gitignore.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\ndist/\nnode_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gi := newGitignoreMatcher(root.Abs)
	reg := NewRegistry()
	reg.Register(&globTool{ws: root, maxMatches: 0, maxBytes: 0, secretPathExceptions: nil, secretPathPatterns: nil, ignore: gi})

	out, err := reg.Execute(context.Background(), "glob", jsonRaw(`{"pattern":"**/*.go"}`))
	if err != nil {
		t.Fatalf("glob error: %v", err)
	}

	// Should find Go files in src/ but NOT in build/ or node_modules/.
	if !contains(out, "src/main.go") {
		t.Errorf("expected src/main.go in glob output:\n%s", out)
	}
	if !contains(out, "src/util.go") {
		t.Errorf("expected src/util.go in glob output:\n%s", out)
	}
	if contains(out, "build/") {
		t.Errorf("build/ should be ignored by .gitignore:\n%s", out)
	}
	if contains(out, "node_modules/") {
		t.Errorf("node_modules/ should be ignored by .gitignore:\n%s", out)
	}
}

func TestGitignoreGrepIntegration(t *testing.T) {
	dir := t.TempDir()
	root, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		"src/app.go",
		"build/cache.go",
		"dist/archive.go",
	} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Write .gitignore.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\ndist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gi := newGitignoreMatcher(root.Abs)
	reg := NewRegistry()
	reg.Register(&grepTool{ws: root, maxMatches: 0, maxBytes: 0, secretPathExceptions: nil, secretPathPatterns: nil, ignore: gi})

	out, err := reg.Execute(context.Background(), "grep", jsonRaw(`{"pattern":"package"}`))
	if err != nil {
		t.Fatalf("grep error: %v", err)
	}

	if !contains(out, "src/app.go") {
		t.Errorf("expected src/app.go in grep output:\n%s", out)
	}
	if contains(out, "build/cache.go") {
		t.Errorf("build/cache.go should be ignored by .gitignore:\n%s", out)
	}
	if contains(out, "dist/archive.go") {
		t.Errorf("dist/archive.go should be ignored by .gitignore:\n%s", out)
	}
}

func TestGitignoreNegation(t *testing.T) {
	dir := t.TempDir()
	root, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create files: important.log should be kept despite *.log rule.
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "important.log"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// .gitignore with negation.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n!important.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gi := newGitignoreMatcher(root.Abs)

	if !gi.MatchRel("debug.log") {
		t.Error("debug.log should be ignored by *.log")
	}
	if gi.MatchRel("important.log") {
		t.Error("important.log should NOT be ignored (negation pattern)")
	}
}

func TestGitignoreNoGitignoreFile(t *testing.T) {
	dir := t.TempDir()
	root, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "anything.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gi := newGitignoreMatcher(root.Abs)
	if gi.MatchRel("anything.txt") {
		t.Error("nothing should be ignored when no .gitignore exists")
	}
	if gi.IsDir("anything") {
		t.Error("no directory should be ignored when no .gitignore exists")
	}
}

func TestGitignoreEmptyRoot(t *testing.T) {
	gi := newGitignoreMatcher("")
	if gi.MatchRel("anything") {
		t.Error("inert matcher should match nothing")
	}
	if gi.IsDir("anything") {
		t.Error("inert matcher IsDir should return false")
	}
}

// TestGitignoreMatcherInertRoot covers the empty-root early-return paths in
// Match, MatchRel and IsDir: an inert matcher (root="") matches nothing and
// never touches the filesystem.
func TestGitignoreMatcherInertRoot(t *testing.T) {
	gi := newGitignoreMatcher("")
	if gi.Match(filepath.Join("anything", "file.txt")) {
		t.Error("inert matcher Match should return false")
	}
	if gi.MatchRel("anything") {
		t.Error("inert matcher MatchRel should return false")
	}
	if gi.IsDir("anything") {
		t.Error("inert matcher IsDir should return false")
	}
}

// TestGitignoreMatchRel pins the MatchRel path: a workspace-relative path is
// joined with the root and matched against the root .gitignore.
func TestGitignoreMatchRel(t *testing.T) {
	dir := t.TempDir()
	root, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\ncache/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gi := newGitignoreMatcher(root.Abs)

	if !gi.MatchRel("debug.log") {
		t.Error("MatchRel should match *.log pattern")
	}
	if !gi.MatchRel("cache/tmp.dat") {
		t.Error("MatchRel should match files under ignored cache/ directory")
	}
	if gi.MatchRel("README.md") {
		t.Error("MatchRel should not match a non-ignored file")
	}
}

func jsonRaw(s string) json.RawMessage { return json.RawMessage(s) }

func contains(s, substr string) bool { return strings.Contains(s, substr) }
