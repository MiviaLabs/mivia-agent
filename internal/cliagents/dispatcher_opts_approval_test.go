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
			// dispatcher anyone runs.
			if len(lit.Elts) == 0 {
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
