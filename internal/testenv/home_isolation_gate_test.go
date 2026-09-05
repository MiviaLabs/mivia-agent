package testenv_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var requiredIsolationPackages = []string{
	"internal/agents",
	"internal/chat",
	"internal/cli",
	"internal/cliagents",
	"internal/clichat",
	"internal/cliorchestrate",
	"internal/cliworkflow",
	"internal/provider",
	"internal/uiadapter",
}

// TestPackagesCallIsolateHomeInTestMain enforces that high-risk packages
// that resolve user configuration, session databases, or credentials
// isolate HOME in TestMain before running tests (DC-42).
func TestPackagesCallIsolateHomeInTestMain(t *testing.T) {
	repoRoot := findRepoRoot(t)

	for _, pkgRel := range requiredIsolationPackages {
		pkgDir := filepath.Join(repoRoot, pkgRel)
		testMainFile, found := findTestMainInDir(t, pkgDir)
		if !found {
			t.Errorf("package %s has no TestMain; must define TestMain calling testenv.IsolateHome", pkgRel)
			continue
		}
		if !testMainCallsIsolateHome(t, testMainFile) {
			t.Errorf("package %s TestMain (in %s) does not call testenv.IsolateHome", pkgRel, filepath.Base(testMainFile))
		}
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

func findTestMainInDir(t *testing.T, dir string) (string, bool) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		fileNode, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range fileNode.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == "TestMain" {
				return path, true
			}
		}
	}
	return "", false
}

func testMainCallsIsolateHome(t *testing.T, path string) bool {
	t.Helper()
	fset := token.NewFileSet()
	fileNode, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	testenvAlias := "testenv"
	for _, imp := range fileNode.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "github.com/MiviaLabs/mivia-agent/internal/testenv" {
			if imp.Name != nil {
				testenvAlias = imp.Name.Name
			}
			break
		}
	}

	callsIsolate := false
	for _, decl := range fileNode.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestMain" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			xIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if xIdent.Name == testenvAlias && sel.Sel.Name == "IsolateHome" {
				callsIsolate = true
				return false
			}
			return true
		})
	}
	return callsIsolate
}
