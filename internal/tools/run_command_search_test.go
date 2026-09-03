package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

func TestGrepNestedAndGlob(t *testing.T) {
	ws, reg := setupWS(t)
	// Nested tree with matches and non-matches.
	paths := map[string]string{
		"root.go":             "package root\nconst Root = 1\n",
		"pkg/a.go":            "package pkg\nfunc Alpha() {}\n",
		"pkg/nested/b.go":     "package nested\nfunc Beta() {}\n",
		"pkg/nested/c.txt":    "no code here Beta word\n",
		"pkg/nested/skip.bin": "ignore",
	}
	for p, body := range paths {
		full := filepath.Join(ws.Abs, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	out, err := reg.Execute(ctx, "grep", json.RawMessage(`{"pattern":"func Beta","glob":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pkg/nested/b.go") {
		t.Fatalf("grep nested: %q", out)
	}
	// glob *.go should not require full path pattern
	out, err = reg.Execute(ctx, "glob", json.RawMessage(`{"pattern":"*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"root.go", "a.go", "b.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("glob missing %s: %q", want, out)
		}
	}
	// grep skips .env-like
	_ = os.WriteFile(filepath.Join(ws.Abs, ".env"), []byte("SECRET_KEY=findme\n"), 0o600)
	out, err = reg.Execute(ctx, "grep", json.RawMessage(`{"pattern":"SECRET_KEY"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "SECRET_KEY") && strings.Contains(out, ".env") {
		t.Fatalf("grep should skip .env: %q", out)
	}
}

func TestGrepMaxMatchesTruncation(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// maxMatches defaults to 0 (uncapped); test with explicit 50.
	reg := NewRegistry()
	reg.Register(&grepTool{ws: ws, maxMatches: 50})
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "match-line-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "many.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := reg.Execute(context.Background(), "grep", json.RawMessage(`{"pattern":"match-line"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "truncated") {
		// 50 max - 100 lines should truncate
		lines := strings.Count(out, "match-line")
		if lines > 50 {
			t.Fatalf("expected truncation, lines=%d out=%q", lines, out)
		}
	}
}

// TestGrepGlobPathForms covers the glob forms a caller actually writes.
//
// The filter matched only the base name, so every path-shaped glob - most
// importantly "**/*.md", the very form the sibling glob tool's description
// recommends - matched nothing and grep looked broken for markdown.
func TestGrepGlobPathForms(t *testing.T) {
	ws, reg := setupWS(t)
	files := map[string]string{
		"README.md":           "# Root\nneedle here\n",
		"docs/guide.md":       "# Guide\nneedle here\n",
		"docs/deep/notes.md":  "# Notes\nneedle here\n",
		"docs/deep/notes.txt": "needle here\n",
		"src/main.go":         "// needle here\n",
	}
	for p, body := range files {
		full := filepath.Join(ws.Abs, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	cases := []struct {
		glob string
		want []string
		deny []string
	}{
		{glob: "*.md", want: []string{"README.md", "docs/guide.md", "docs/deep/notes.md"}, deny: []string{"notes.txt", "main.go"}},
		{glob: "**/*.md", want: []string{"README.md", "docs/guide.md", "docs/deep/notes.md"}, deny: []string{"notes.txt", "main.go"}},
		{glob: "docs/**/*.md", want: []string{"docs/guide.md", "docs/deep/notes.md"}, deny: []string{"README.md", "main.go"}},
		{glob: "*.MD", want: []string{"README.md", "docs/guide.md", "docs/deep/notes.md"}, deny: []string{"main.go"}},
		{glob: "src/*.go", want: []string{"src/main.go"}, deny: []string{"README.md"}},
	}
	for _, tc := range cases {
		payload := fmt.Sprintf(`{"pattern":"needle","glob":%q}`, tc.glob)
		out, err := reg.Execute(ctx, "grep", json.RawMessage(payload))
		if err != nil {
			t.Fatalf("glob %q: %v", tc.glob, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Fatalf("glob %q missing %s in:\n%s", tc.glob, w, out)
			}
		}
		for _, d := range tc.deny {
			if strings.Contains(out, d) {
				t.Fatalf("glob %q wrongly matched %s in:\n%s", tc.glob, d, out)
			}
		}
	}
}

// TestGlobToolPathForms pins that the glob tool agrees with grep's filter.
// Two matchers meant "**/*.md" behaved differently in the two tools, and
// "docs/**/*.md" missed anything deeper than one level.
func TestGlobToolPathForms(t *testing.T) {
	ws, reg := setupWS(t)
	for _, p := range []string{"README.md", "docs/guide.md", "docs/deep/notes.md", "src/main.go"} {
		full := filepath.Join(ws.Abs, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	cases := []struct {
		pattern string
		want    []string
		deny    []string
	}{
		{pattern: "**/*.md", want: []string{"README.md", "docs/guide.md", "docs/deep/notes.md"}, deny: []string{"main.go"}},
		{pattern: "docs/**/*.md", want: []string{"docs/guide.md", "docs/deep/notes.md"}, deny: []string{"README.md"}},
	}
	for _, tc := range cases {
		out, err := reg.Execute(ctx, "glob", json.RawMessage(fmt.Sprintf(`{"pattern":%q}`, tc.pattern)))
		if err != nil {
			t.Fatalf("pattern %q: %v", tc.pattern, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(out, w) {
				t.Fatalf("pattern %q missing %s in:\n%s", tc.pattern, w, out)
			}
		}
		for _, d := range tc.deny {
			if strings.Contains(out, d) {
				t.Fatalf("pattern %q wrongly matched %s in:\n%s", tc.pattern, d, out)
			}
		}
	}
}
