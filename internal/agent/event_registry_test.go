package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// TestAllEventKindsMatchesTheDeclaredConstants reads the package's own source
// and proves the registry names every EventKind constant, and no other.
//
// A registry that is itself a hand list only moves the problem: the whole
// point is that adding a constant must fail something. Parsing is what makes
// that true, because the constant declaration is the thing being added.
func TestAllEventKindsMatchesTheDeclaredConstants(t *testing.T) {
	declared := parseDeclaredEventKinds(t)
	if len(declared) == 0 {
		t.Fatal("no EventKind constant was found; the parse is wrong, not the code")
	}

	registered := map[string]bool{}
	for _, k := range AllEventKinds() {
		if registered[string(k)] {
			t.Errorf("AllEventKinds lists %q twice", k)
		}
		registered[string(k)] = true
	}

	for name, value := range declared {
		if !registered[value] {
			t.Errorf("%s (%q) is declared but missing from AllEventKinds, so every "+
				"exhaustiveness test built on it silently skips this kind", name, value)
		}
		delete(registered, value)
	}
	for leftover := range registered {
		t.Errorf("AllEventKinds lists %q, which no constant declares", leftover)
	}
}

// parseDeclaredEventKinds returns constant name -> string value for every
// `X EventKind = "..."` declaration in this package's non-test files.
func parseDeclaredEventKinds(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package source: %v", err)
	}

	out := map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				spec, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				ident, ok := spec.Type.(*ast.Ident)
				if !ok || ident.Name != "EventKind" {
					return true
				}
				for i, name := range spec.Names {
					if i >= len(spec.Values) {
						continue
					}
					lit, ok := spec.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					out[name.Name] = lit.Value[1 : len(lit.Value)-1]
				}
				return true
			})
		}
	}
	return out
}
