package chat

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestClichatDoesNotImportBubbletea pins that production clichat (the
// --plain REPL and shared helpers) must not import bubbletea or bubbles.
// Interactive TTY chat launches via tui. Lipgloss is allowed: it styles
// line-mode chrome, not a compositor.
func TestClichatDoesNotImportBubbletea(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, e.Name(), src, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(path, "bubbletea") || strings.Contains(path, "bubbles") {
				t.Errorf("%s imports %q: internal/cli/chat production code must not depend on bubbletea", e.Name(), path)
			}
		}
	}
}
