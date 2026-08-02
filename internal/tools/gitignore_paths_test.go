package tools

// The matcher's inert and failure paths: a nil matcher, an empty root, and a
// .gitignore that cannot be read or cannot be compiled. Each must leave the
// matcher matching nothing rather than failing a walk.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNilMatcherIsInert(t *testing.T) {
	var g *gitignoreMatcher
	if view := g.snapshot(); view.m != nil || view.root != "" || view.patterns != nil {
		t.Fatalf("nil matcher snapshot = %+v, want the zero view", view)
	}
	if patterns := g.Patterns(); patterns != nil {
		t.Fatalf("nil matcher Patterns() = %v, want nil", patterns)
	}
}

func TestRootlessMatcherCompilesNothing(t *testing.T) {
	g := newIgnoreSource("", []string{"node_modules"})
	view := g.snapshot()
	if view.m != nil {
		t.Fatal("a rootless matcher compiled gitignore rules")
	}
	if got := g.Patterns(); len(got) != 1 || got[0] != "node_modules" {
		t.Fatalf("Patterns() = %v, want the configured floor", got)
	}
}

func TestUnreadableGitignoreMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	// A directory named .gitignore stats fine and cannot be read.
	if err := os.MkdirAll(filepath.Join(dir, ".gitignore"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := newGitignoreMatcher(dir)
	if view := g.snapshot(); view.m != nil {
		t.Fatal("an unreadable .gitignore produced compiled rules")
	}
	if g.MatchRel("anything") {
		t.Fatal("an unreadable .gitignore matched a path")
	}
}

func TestUncompilableGitignoreMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("[a-\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := newGitignoreMatcher(dir)
	if view := g.snapshot(); view.m != nil {
		t.Fatal("an uncompilable .gitignore produced compiled rules")
	}
	if g.IsDir("build") {
		t.Fatal("an uncompilable .gitignore matched a directory")
	}
}

func TestSplitGitignoreLinesOnAnEmptyFile(t *testing.T) {
	if got := splitGitignoreLines(nil); got != nil {
		t.Fatalf("splitGitignoreLines(nil) = %v, want nil", got)
	}
	if got := splitGitignoreLines([]byte("a\r\nb\n")); len(got) != 3 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("splitGitignoreLines = %q, want CRLF normalized lines", got)
	}
}
