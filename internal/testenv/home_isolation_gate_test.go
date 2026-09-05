package testenv_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	configImportPath    = "github.com/MiviaLabs/mivia-agent/internal/config"
	workspaceImportPath = "github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// knownBaselinePackages lists packages known to require isolation.
// Dynamic discovery must find all packages in this list.
var knownBaselinePackages = []string{
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

// exemptPackages lists packages that do not require TestMain home isolation.
// internal/config defines config primitives and isolates HOME per subtest.
var exemptPackages = map[string]string{
	"internal/config": "defines configuration primitives; isolates HOME per subtest",
}

var testConfigFunctions = map[string]bool{
	"Load":                           true,
	"LoadAgentsGlobal":               true,
	"LoadTrustedMCPConfig":           true,
	"LoadHooksSource":                true,
	"LoadProviderDefaultOverrides":   true,
	"LoadScopeMCPServers":            true,
	"UserConfigPath":                 true,
	"UserEnvPath":                    true,
	"UserAuthPath":                   true,
	"UserAgentsDir":                  true,
	"UserFullDiskAccessForWorkspace": true,
	"SetUserFullDiskAccess":          true,
	"DefaultConfigCandidates":        true,
	"DefaultEnvCandidates":           true,
	"AutoBootstrapUserConfig":        true,
	"WriteDefaultUserConfig":         true,
	"WriteUserEnvKey":                true,
}

var testWorkspaceFunctions = map[string]bool{
	"GlobalContextStorePath": true,
	"GlobalMemoryDir":        true,
	"UserSkillsDir":          true,
	"UserHomeDir":            true,
}

var prodConfigFunctions = map[string]bool{
	"Load":                 true,
	"LoadAgentsGlobal":     true,
	"LoadTrustedMCPConfig": true,
}

// TestPackagesCallIsolateHomeInTestMain discovers packages that load user
// configuration or access ambient user state. The test verifies that each
// package defines TestMain and calls testenv.IsolateHome before tests run (DC-42).
func TestPackagesCallIsolateHomeInTestMain(t *testing.T) {
	repoRoot := findRepoRoot(t)

	discovered, err := discoverPackagesLoadingUserConfig(repoRoot)
	if err != nil {
		t.Fatalf("discover packages: %v", err)
	}

	discoveredSet := make(map[string]bool, len(discovered))
	for _, pkg := range discovered {
		discoveredSet[pkg] = true
	}

	for _, baseline := range knownBaselinePackages {
		if !discoveredSet[baseline] {
			t.Errorf("dynamic discovery missed baseline package %s", baseline)
		}
	}

	for _, pkgRel := range discovered {
		if reason, exempt := exemptPackages[pkgRel]; exempt {
			t.Logf("skipping exempt package %s: %s", pkgRel, reason)
			continue
		}
		pkgDir := filepath.Join(repoRoot, pkgRel)
		testMainFile, found := findTestMainInDir(t, pkgDir)
		if !found {
			t.Errorf("package %s loads user configuration but has no TestMain; must define TestMain calling testenv.IsolateHome", pkgRel)
			continue
		}
		if !testMainCallsIsolateHome(t, testMainFile) {
			t.Errorf("package %s TestMain (in %s) does not call testenv.IsolateHome", pkgRel, filepath.Base(testMainFile))
		}
	}
}

func discoverPackagesLoadingUserConfig(repoRoot string) ([]string, error) {
	var matching []string
	scanRoots := []string{
		filepath.Join(repoRoot, "cmd"),
		filepath.Join(repoRoot, "internal"),
	}
	for _, scanRoot := range scanRoots {
		err := filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			loads, err := packageLoadsUserConfig(path)
			if err != nil {
				return err
			}
			if loads {
				rel, err := filepath.Rel(repoRoot, path)
				if err != nil {
					return err
				}
				matching = append(matching, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(matching)
	return matching, nil
}

func packageLoadsUserConfig(pkgDir string) (bool, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return false, err
	}
	var testFiles []string
	var prodFiles []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		path := filepath.Join(pkgDir, entry.Name())
		if strings.HasSuffix(entry.Name(), "_test.go") {
			testFiles = append(testFiles, path)
		} else {
			prodFiles = append(prodFiles, path)
		}
	}
	if len(testFiles) == 0 {
		return false, nil
	}
	return inspectPackageFiles(testFiles, prodFiles)
}

func inspectPackageFiles(testFiles, prodFiles []string) (bool, error) {
	for _, tf := range testFiles {
		callsConfig, err := fileCallsTarget(tf, configImportPath, testConfigFunctions)
		if err != nil {
			return false, err
		}
		if callsConfig {
			return true, nil
		}
		callsWS, err := fileCallsTarget(tf, workspaceImportPath, testWorkspaceFunctions)
		if err != nil {
			return false, err
		}
		if callsWS {
			return true, nil
		}
	}
	for _, pf := range prodFiles {
		callsLoad, err := fileCallsTarget(pf, configImportPath, prodConfigFunctions)
		if err != nil {
			return false, err
		}
		if callsLoad {
			return true, nil
		}
	}
	return false, nil
}

func fileCallsTarget(path, importPath string, funcNames map[string]bool) (bool, error) {
	fset := token.NewFileSet()
	fileNode, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return false, err
	}
	alias, imported := importAlias(fileNode, importPath)
	if !imported {
		return false, nil
	}
	return fileCallsAnySelector(fileNode, alias, funcNames), nil
}

func importAlias(fileNode *ast.File, importPath string) (string, bool) {
	for _, imp := range fileNode.Imports {
		if strings.Trim(imp.Path.Value, `"`) == importPath {
			if imp.Name != nil {
				return imp.Name.Name, true
			}
			parts := strings.Split(importPath, "/")
			return parts[len(parts)-1], true
		}
	}
	return "", false
}

func fileCallsAnySelector(fileNode *ast.File, pkgAlias string, funcNames map[string]bool) bool {
	found := false
	ast.Inspect(fileNode, func(n ast.Node) bool {
		if found {
			return false
		}
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
		if xIdent.Name == pkgAlias && funcNames[sel.Sel.Name] {
			found = true
			return false
		}
		return true
	})
	return found
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
