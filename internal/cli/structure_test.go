package cli

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// maxFileLines is the maximum allowed lines for any single .go file
// in internal/cli/.
const maxFileLines = 800

// TestStructure_Baseline checks structural invariants for internal/cli/:
//   - Package compiles without errors (via go/parser)
//   - No .go file exceeds 800 lines
//
// tui.go and its line-count ceiling moved to internal/legacytui/ with the
// rest of the TUI: this package no longer has a tui.go to check.
func TestStructure_Baseline(t *testing.T) {
	t.Run("package compiles (go/parser)", func(t *testing.T) {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, ".", nil, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse package directory: %v", err)
		}
		if _, ok := pkgs["cli"]; !ok {
			t.Fatal("package cli not found after parsing")
		}
		// Also parse each .go file individually for granular error reporting.
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			_, err := parser.ParseFile(fset, e.Name(), nil, parser.AllErrors)
			if err != nil {
				t.Errorf("parse error in %s: %v", e.Name(), err)
			}
		}
	})

	t.Run("no file exceeds max lines", func(t *testing.T) {
		entries, err := os.ReadDir(".")
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			lines, err := countLines(e.Name())
			if err != nil {
				t.Errorf("reading %s: %v", e.Name(), err)
				continue
			}
			if lines > maxFileLines {
				t.Errorf("%s has %d lines, exceeds maximum of %d", e.Name(), lines, maxFileLines)
			}
		}
	})
}

// countLines returns the number of lines in a file within the current directory.
func countLines(name string) (int, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}
	n := 1 // last line may not have trailing newline
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	// If file ends with '\n', the final newline does not start an extra line.
	if len(data) > 0 && data[len(data)-1] == '\n' {
		n--
	}
	return n, nil
}
