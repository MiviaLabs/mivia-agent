package evidencecheck

// GATE: every exec in this repository bounds the wait for inherited pipes.
//
// The class, named by mechanism: os/exec's Wait does not return while ANY
// process holds the child's inherited stdout/stderr pipe. Killing the child,
// or cancelling its context, does not touch the grandchildren it spawned -
// ssh and credential helpers under git push, post-checkout hooks and smudge
// filters under git worktree add, gh's own subprocesses, an MCP server's
// helpers. Without cmd.WaitDelay the call can outlive every deadline the
// caller set.
//
// That is not merely slow. A Deliver that never returns leaves its run
// in-flight forever, because engine_deliver defers both the delivering-map
// delete and the run release; cancel then refuses it, delete refuses it, and
// interrupt skips its fence. One hung pipe, one permanently stuck run.
//
// This gate walks the AST of every non-test file under internal/ and requires
// each exec.Command / exec.CommandContext result to have WaitDelay assigned in
// the same function, or to be listed in the exemption table below with the
// reason it cannot leave a grandchild behind.
//
// What it CANNOT catch: a WaitDelay assigned a useless value, a command whose
// result escapes the function before the bound is set, and any exec outside
// internal/. It checks that the bound is established where the command is
// built, which is where every instance of this class has been.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// execWaitDelayExemptions maps "package/file.go:funcName" to the reason that
// exec genuinely cannot strand a grandchild on the pipe. Keep this table
// small; an entry is a claim about process lifetime, not a convenience.
var execWaitDelayExemptions = map[string]string{
	"definition/sandbox.go:runSandboxedCommand": "bwrap runs --unshare-all --die-with-parent, so nothing inside the sandbox outlives it and no grandchild survives to hold the pipe.",
}

func TestEveryExecBoundsItsPipeWait(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Errorf("parse %s: %v", path, perr)
			return nil
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			checkFuncExecBounds(t, path, fn)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}
}

// checkFuncExecBounds reports an exec built in fn whose result never has
// WaitDelay assigned inside the same function.
func checkFuncExecBounds(t *testing.T, path string, fn *ast.FuncDecl) {
	t.Helper()
	built := map[string]bool{}   // variable name -> built by exec.Command here
	bounded := map[string]bool{} // variable name -> WaitDelay assigned here

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range assign.Rhs {
			if !isExecCommandCall(rhs) || i >= len(assign.Lhs) {
				continue
			}
			if ident, ok := assign.Lhs[i].(*ast.Ident); ok {
				built[ident.Name] = true
			}
		}
		// `x.WaitDelay = ...`
		for _, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WaitDelay" {
				continue
			}
			if ident, ok := sel.X.(*ast.Ident); ok {
				bounded[ident.Name] = true
			}
		}
		return true
	})

	key := filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path) + ":" + fn.Name.Name
	for name := range built {
		if bounded[name] {
			continue
		}
		if reason, exempt := execWaitDelayExemptions[key]; exempt {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s exempts an exec with an empty reason", key)
			}
			continue
		}
		t.Errorf("%s builds %q with exec.Command but never sets %s.WaitDelay.\n"+
			"  Wait will not return while a grandchild holds the inherited pipe,\n"+
			"  and cancelling the context kills only the direct child. Set\n"+
			"  %s.WaitDelay, or add %q to execWaitDelayExemptions with the reason\n"+
			"  this command cannot leave a grandchild behind.",
			key, name, name, name, key)
	}
}

// isExecCommandCall reports whether e is exec.Command(...) or
// exec.CommandContext(...).
func isExecCommandCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "exec" {
		return false
	}
	return sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"
}
