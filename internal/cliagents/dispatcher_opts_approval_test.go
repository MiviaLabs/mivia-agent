package cliagents

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

// Every construction of SessionDispatcherOpts must set Approval.
//
// The opts struct is what a nested subagent loop's approval wiring travels
// in, and it is REBUILT on /agent, /model, /new, /resume, and whenever the
// model calls load_tools. The first version of the subagent gating fix set it
// at one construction site and missed the rebuilds, so delegation was gated
// for the first turn of a session and ungated from the moment anything
// rebuilt the dispatcher - including a rebuild the model itself can trigger.
//
// A sweep for `MultiStepHandler{` could not see that: the handler is built
// from these opts, several layers down. So the check is on the struct.
//
// This is a source check rather than a behavioural one because the failure is
// an OMISSION at a construction site, and no runtime path exercises every
// site. It reads the module's own source, so a new site added tomorrow is
// covered without anyone remembering this file exists.
func TestEverySessionDispatcherOptsCarriesApproval(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()

	var missing []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // A file this test cannot parse is not this test's finding.
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isSessionDispatcherOpts(lit.Type) {
				return true
			}
			// An empty literal is a zero value for an error return, not a
			// dispatcher anyone runs - but ONLY when nothing later assigns
			// fields onto it. `opts := SessionDispatcherOpts{}` followed by
			// `opts.Registry = ...` is a fully-built dispatcher that this
			// exemption used to wave through, and the shape is already
			// idiomatic in this tree, so it would not look wrong in review.
			if len(lit.Elts) == 0 {
				if assigned := fieldsAssignedOnZeroLiteral(file, lit); len(assigned) > 0 {
					if !assigned["Approval"] {
						missing = append(missing, fmt.Sprintf("%s:%d (zero literal then %d field assignment(s), no Approval)",
							path, fset.Position(lit.Pos()).Line, len(assigned)))
					}
				}
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Approval" {
					return true
				}
			}
			rel, _ := filepath.Rel(root, path)
			missing = append(missing, fmt.Sprintf("%s:%d", rel, fset.Position(lit.Pos()).Line))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(missing) > 0 {
		t.Errorf("SessionDispatcherOpts built without Approval at %v.\n"+
			"Every one of these replaces a session's dispatcher, and a nested "+
			"subagent loop built from it runs UNGATED: a write tool the operator "+
			"would be asked about on the root path executes unprompted the moment "+
			"the model delegates it. Pass the session's ApprovalSnapshot, or for a "+
			"headless runner the configured policy.", missing)
	}
}

func isSessionDispatcherOpts(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "SessionDispatcherOpts"
	case *ast.SelectorExpr:
		return t.Sel.Name == "SessionDispatcherOpts"
	}
	return false
}

// moduleRoot walks up to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
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

// fieldsAssignedOnZeroLiteral returns the field names assigned onto the
// variable a zero SessionDispatcherOpts literal was bound to.
//
// It exists because the empty-literal exemption above is only safe for a
// genuine zero value. A review pointed out that `opts := SessionDispatcherOpts{}`
// followed by per-field assignments is fully exempt, and that is a complete
// bypass: the dispatcher is built, subagent delegation runs, and no Approval
// is ever set.
func fieldsAssignedOnZeroLiteral(file *ast.File, lit *ast.CompositeLit) map[string]bool {
	// Find the identifier the literal is assigned to.
	target := ""
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		rhs := assign.Rhs[0]
		if unary, ok := rhs.(*ast.UnaryExpr); ok {
			rhs = unary.X
		}
		if rhs != ast.Expr(lit) {
			return true
		}
		if id, ok := assign.Lhs[0].(*ast.Ident); ok {
			target = id.Name
		}
		return true
	})
	if target == "" {
		return nil
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == target {
				out[sel.Sel.Name] = true
			}
		}
		return true
	})
	return out
}
