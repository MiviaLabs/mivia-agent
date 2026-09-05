package localengine_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalEngineHasNoProductionConstructionSites proves that localengine.Engine
// has no production construction sites outside of its own package.
//
// localengine.Engine is unhardened against MCP digest mismatch on resume
// (unlike cliworkflow.sessionWorkflowEngine which verifies the MCP digest
// against the admitted snapshot). Until localengine.Engine implements the same
// check, it must not be constructed in production code paths.
func TestLocalEngineHasNoProductionConstructionSites(t *testing.T) {
	repoRoot := findRepoRoot(t)
	searchDirs := []string{
		filepath.Join(repoRoot, "cmd"),
		filepath.Join(repoRoot, "internal"),
	}

	violations, err := findEngineViolations(repoRoot, searchDirs)
	if err != nil {
		t.Fatalf("scan production engine sites: %v", err)
	}

	if len(violations) > 0 {
		t.Fatalf("found %d production construction/reference site(s) of localengine.Engine:\n%s\n"+
			"localengine.Engine lacks MCP digest validation on resume and must not be used in production",
			len(violations), strings.Join(violations, "\n"))
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	curr := dir
	for {
		if _, err := os.Stat(filepath.Join(curr, "go.mod")); err == nil {
			return curr
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			break
		}
		curr = parent
	}
	t.Fatal("could not find repo root containing go.mod")
	return ""
}

func findEngineViolations(repoRoot string, searchDirs []string) ([]string, error) {
	fset := token.NewFileSet()
	var violations []string

	for _, searchDir := range searchDirs {
		err := filepath.WalkDir(searchDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "localengine" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			v, err := inspectFileForEngine(fset, path)
			if err != nil {
				return err
			}
			violations = append(violations, v...)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", searchDir, err)
		}
	}
	return violations, nil
}

func inspectFileForEngine(fset *token.FileSet, path string) ([]string, error) {
	fileNode, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	localName := ""
	for _, imp := range fileNode.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "github.com/MiviaLabs/mivia-agent/internal/workflows/localengine" {
			if imp.Name != nil {
				localName = imp.Name.Name
			} else {
				localName = "localengine"
			}
			break
		}
	}
	if localName == "" {
		return nil, nil
	}

	var found []string
	ast.Inspect(fileNode, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != localName {
			return true
		}
		if sel.Sel.Name == "Engine" {
			pos := fset.Position(sel.Pos())
			found = append(found, fmt.Sprintf("%s:%d", pos.Filename, pos.Line))
		}
		return true
	})
	return found, nil
}
