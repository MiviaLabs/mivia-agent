package newtui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryPoolStreamIsWiredIntoTheScreen gates the defect class that both
// halves of the workflow-progress work turned out to be instances of: a
// signal is produced, transported, and offered as a stream, and nothing at
// the far end ever reads it.
//
// ports.Notices sat that way for its whole existence - a producer on the
// adapter side, no reader anywhere in internal/ui - so chat-sync advisories
// went into a channel nobody drained, and wiring the workflow subscription
// alone would only have moved the silence one layer. WorkflowStatus is the
// second such stream, and an unwired third would fail exactly the same way:
// silently, with no error and no test.
//
// So: every SessionPool method that hands out a stream must be named in
// buildApp. The scan reads both sources rather than any hand-kept list,
// because a list is one more place to forget.
func TestEveryPoolStreamIsWiredIntoTheScreen(t *testing.T) {
	streams := poolStreamMethods(t)
	if len(streams) < 2 {
		t.Fatalf("found %d pool stream methods; the scan is broken, not the wiring", len(streams))
	}
	wiring := poolCallsInBuildApp(t)
	for _, name := range streams {
		if !wiring[name] {
			t.Errorf("SessionPool.%s returns a stream that buildApp never wires into the screen: "+
				"a producer with no reader fails silently, which is the whole reason this test exists", name)
		}
	}
}

// poolStreamMethods returns the exported *SessionPool methods whose only
// result is a receive-only channel - the pool's outbound streams.
func poolStreamMethods(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "uiadapter")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, streamMethodsInFile(t, filepath.Join(dir, name))...)
	}
	return out
}

func streamMethodsInFile(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || !fn.Name.IsExported() {
			continue
		}
		if !isSessionPoolReceiver(fn.Recv) || fn.Type.Params.NumFields() != 0 {
			continue
		}
		if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		if ch, ok := fn.Type.Results.List[0].Type.(*ast.ChanType); ok && ch.Dir == ast.RECV {
			out = append(out, fn.Name.Name)
		}
	}
	return out
}

func isSessionPoolReceiver(recv *ast.FieldList) bool {
	if recv.NumFields() != 1 {
		return false
	}
	star, ok := recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "SessionPool"
}

// poolCallsInBuildApp returns the set of methods buildApp actually CALLS on
// the pool.
//
// The AST, not the file text: an earlier version of this test grepped the
// source, and the doc comment above each wiring line names the very method
// the line calls - so deleting the call left the comment behind and the gate
// passed. A test that a comment can satisfy proves nothing.
func poolCallsInBuildApp(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "run.go", nil, 0)
	if err != nil {
		t.Fatalf("parse run.go: %v", err)
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "pool" {
			out[sel.Sel.Name] = true
		}
		return true
	})
	return out
}
