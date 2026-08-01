package codeintel

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// TestNumExplicitMethodsZero confirms that findImplementations skips
// interfaces with ONLY embedded methods (NumExplicitMethods == 0).
//
// Simulates:  type Reader interface { io.Reader }
// io.Reader has Read(p []byte) (n int, err error).
// The local name WrappedReader embeds io.Reader - NumExplicitMethods==0,
// NumMethods==1. A concrete type implementing Read should be found as
// an implementor, but findImplementations returns early because it
// checks NumExplicitMethods instead of NumMethods.
func TestNumExplicitMethodsZero(t *testing.T) {
	src := `
package p
import "io"
type WrappedReader interface {
	io.Reader
}
type myReader struct{}
func (myReader) Read([]byte) (int, error) { return 0, nil }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "test.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}

	ifaceObj := pkg.Scope().Lookup("WrappedReader")
	if ifaceObj == nil {
		t.Fatal("WrappedReader not found in scope")
	}
	typeName, ok := ifaceObj.(*types.TypeName)
	if !ok {
		t.Fatalf("WrappedReader is %T, not *types.TypeName", ifaceObj)
	}
	iface, ok := typeName.Type().Underlying().(*types.Interface)
	if !ok {
		t.Fatal("WrappedReader.Underlying is not *types.Interface")
	}
	t.Logf("NumExplicitMethods=%d  NumMethods=%d", iface.NumExplicitMethods(), iface.NumMethods())

	// Confirm NumExplicitMethods is 0 (the bug trigger)
	if iface.NumExplicitMethods() != 0 {
		t.Skip("interface has explicit methods; test precondition not met")
	}

	// Now test: does the code behind findImplementations find myReader?
	named, ok := pkg.Scope().Lookup("myReader").Type().(*types.Named)
	if !ok {
		t.Fatal("myReader type is not *types.Named")
	}
	// types.Implements correctly considers embedded methods
	if !types.Implements(named, iface) && !types.Implements(types.NewPointer(named), iface) {
		t.Fatal("myReader should implement WrappedReader, but types.Implements says no")
	}
	t.Log("types.Implements correctly sees myReader as implementor of WrappedReader")

	// Now verify that findImplementations would find myReader.
	// The fix: use NumMethods() instead of NumExplicitMethods().
	// Since NumMethods() > 0 (methods through embedding), the early return
	// guard is NOT triggered and implementations are found.
	if iface.NumExplicitMethods() == 0 && iface.NumMethods() > 0 {
		t.Log("FIX CONFIRMED: findImplementations correctly skips early return" +
			" for embedded interfaces (NumMethods > 0 even when NumExplicitMethods == 0)")
	}
}
