package hooks

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenImports are the packages internal/hooks must never reach.
//
// If hooks could reach internal/runtime or internal/tools, a PreToolUse hook
// matching run_command could dispatch run_command, which would fire PreToolUse,
// which would dispatch run_command - unbounded recursion on the first hook
// anyone writes. Policy.MaxDepth would not catch it, because hook execution
// carries no depth. Hooks are out-of-band process execution, and this test is
// what keeps that true across refactors.
var forbiddenImports = []string{
	"github.com/MiviaLabs/mivia-agent/internal/runtime",
	"github.com/MiviaLabs/mivia-agent/internal/tools",
}

func TestHooksPackageDoesNotImportRuntimeOrTools(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatalf("%s: bad import literal %s", name, spec.Path.Value)
				}
				for _, bad := range forbiddenImports {
					if path == bad || strings.HasPrefix(path, bad+"/") {
						t.Errorf("%s imports %s; hooks must never re-enter the dispatcher", name, path)
					}
				}
			}
		}
	}
}

// The direct-import check above can be defeated by an intermediate package, so
// assert the transitive graph too.
func TestHooksTransitiveDepsExcludeRuntimeAndTools(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	cmd := exec.Command("go", "list", "-deps", ".")
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		dep := strings.TrimSpace(line)
		for _, bad := range forbiddenImports {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				t.Errorf("internal/hooks transitively depends on %s", dep)
			}
		}
	}
}

// The package must not grow a shell out from under the argv-only contract.
func TestHooksPackageNamesNoShell(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	banned := []string{`"sh"`, `"/bin/sh"`, `"bash"`, `"/bin/bash"`, `"cmd.exe"`, `"powershell"`, "exec.LookPath"}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, bad := range banned {
			if strings.Contains(string(data), bad) {
				t.Errorf("%s references %s; hooks run an explicit argv with no shell and no PATH lookup", name, bad)
			}
		}
	}
}
