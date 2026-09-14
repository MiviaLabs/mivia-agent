package cli

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// maxFileLines is the maximum allowed lines for any single .go file
// in internal/cli/.
const maxFileLines = 800

// TestStructure_Baseline checks structural invariants for the internal/cli/
// tree (root package plus the worktree/agents/orchestrate/workflow/chat/
// automations subpackages regrouped here):
//   - Package compiles without errors (via go/parser)
//   - No .go file anywhere in the tree exceeds 800 lines
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
		for _, name := range goFilesInTree(t, ".") {
			_, err := parser.ParseFile(fset, name, nil, parser.AllErrors)
			if err != nil {
				t.Errorf("parse error in %s: %v", name, err)
			}
		}
	})

	t.Run("no file exceeds max lines", func(t *testing.T) {
		for _, name := range goFilesInTree(t, ".") {
			lines, err := countLines(name)
			if err != nil {
				t.Errorf("reading %s: %v", name, err)
				continue
			}
			if lines > maxFileLines {
				t.Errorf("%s has %d lines, exceeds maximum of %d", name, lines, maxFileLines)
			}
		}
	})
}

// goFilesInTree returns every .go file under root, recursively, so the
// invariants cover the subpackages grouped under internal/cli/ and not just
// the root command-wiring package.
func goFilesInTree(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
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
