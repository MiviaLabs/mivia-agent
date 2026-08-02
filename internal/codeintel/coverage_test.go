package codeintel

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestPosInfoHandlesMissingAndValidPositions(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", "package sample\nvar value = 1\n", 0)
	if err != nil {
		t.Fatal(err)
	}

	if path, line := posInfo(nil, file); path != "" || line != 0 {
		t.Errorf("nil fileset = %q:%d, want empty position", path, line)
	}
	if path, line := posInfo(fset, ast.NewIdent("value")); path != "" || line != 0 {
		t.Errorf("NoPos = %q:%d, want empty position", path, line)
	}
	other := token.NewFileSet()
	if path, line := posInfo(other, file); path != "" || line != 0 {
		t.Errorf("foreign fileset = %q:%d, want empty position", path, line)
	}
	if path, line := posInfo(fset, file); path != "sample.go" || line != 1 {
		t.Errorf("valid position = %q:%d, want sample.go:1", path, line)
	}
}

func TestClassifyUseRoleRecognizesReturnAndComparison(t *testing.T) {
	const source = `package sample
var target error
func f(err error) error {
	if err == target { return target }
	return err
}`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := &packages.Package{Syntax: []*ast.File{nil, file}}
	if got := classifyUseRole(nil, pkg); got != RoleCaller {
		t.Errorf("nil identifier role = %q, want %q", got, RoleCaller)
	}
	if got := classifyUseRole(ast.NewIdent("missing"), pkg); got != RoleCaller {
		t.Errorf("unmatched identifier role = %q, want %q", got, RoleCaller)
	}

	roles := map[Role]int{}
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == "target" {
			roles[classifyUseRole(id, pkg)]++
		}
		return true
	})
	if roles[RoleComparison] != 1 || roles[RoleReturn] != 1 {
		t.Fatalf("target roles = %#v, want one comparison and one return", roles)
	}
}
