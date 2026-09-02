package clichat

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

// A function that wires Session.ToolBaseResolver must also set
// Session.ToolDenylist.
//
// ToolBaseResolver hands the deferred-tool path the PRE-scope registry: the
// full set, before the operator's mandatory denylist has been applied to it.
// Every other layer refuses a denied name - ScopedRegistry drops it from the
// executable registry, ScopedRegistryWithTail refuses to admit it - so the
// resolver is the one door left, and ToolDenylist is what closes it.
//
// The enforcement itself lives at the point of execution (lookupDeferredTool),
// so an unset field still applies the COMPILED denylist and fails safe. What
// it silently loses is the OPERATOR's additions, which is the half nobody
// would notice: the tool runs, and nothing anywhere says a guardrail was
// skipped.
//
// A source check rather than a behavioural one, for the reason the sibling
// gate in internal/cliagents gives: the failure is an omission at a wiring
// site, and no runtime path exercises every site.
func TestWiringToolBaseResolverAlsoSetsTheDenylist(t *testing.T) {
	root := repoRootForGate(t)
	fset := token.NewFileSet()
	var missing []string

	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}
			var wiresResolver, setsDenylist bool
			var resolverRecv, denylistRecv string
			var line int
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				assign, ok := inner.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for i, lhs := range assign.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok {
						continue
					}
					// The receiver must match, or `opts.ToolDenylist = x` on
					// some unrelated struct satisfies the check while
					// sess.ToolDenylist is never set.
					recv, ok := sel.X.(*ast.Ident)
					if !ok {
						continue
					}
					switch sel.Sel.Name {
					case "ToolBaseResolver":
						wiresResolver = true
						resolverRecv = recv.Name
						line = fset.Position(sel.Pos()).Line
					case "ToolDenylist":
						// VALUE, not presence: `sess.ToolDenylist = nil`
						// satisfied the old check completely, and that is the
						// whole defect - the operator's additions are dropped,
						// the compiled list still applies, and nothing looks
						// broken.
						if i < len(assign.Rhs) && isNilOrEmptyLiteral(assign.Rhs[i]) {
							continue
						}
						denylistRecv = recv.Name
						setsDenylist = true
					}
				}
				return true
			})
			if wiresResolver && (!setsDenylist || resolverRecv != denylistRecv) {
				rel, _ := filepath.Rel(root, path)
				missing = append(missing, fmt.Sprintf("%s:%d (%s)", rel, line, fn.Name.Name))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(missing) > 0 {
		t.Errorf("these wire ToolBaseResolver without setting ToolDenylist: %v\n"+
			"The resolver hands the deferred-tool path the pre-scope registry, "+
			"which the operator's mandatory denylist has not been applied to. "+
			"Set Session.ToolDenylist from the same denylist the surrounding "+
			"scope call uses, or the operator's additions are dropped for every "+
			"deferred call.", missing)
	}
}

func repoRootForGate(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// isNilOrEmptyLiteral reports whether an assignment's right-hand side is a
// value that sets nothing: nil, []string{}, or []string(nil).
//
// Assigning one of those satisfied the presence check while dropping the
// operator's additions entirely - the compiled denylist still applies, so the
// session looks healthy and the guardrail is gone.
func isNilOrEmptyLiteral(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name == "nil"
	case *ast.CompositeLit:
		return len(v.Elts) == 0
	case *ast.CallExpr:
		// []string(nil)
		if len(v.Args) == 1 {
			if id, ok := v.Args[0].(*ast.Ident); ok && id.Name == "nil" {
				return true
			}
		}
	}
	return false
}
