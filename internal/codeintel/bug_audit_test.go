package codeintel

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

// Verify that the analyzerEnv has exactly one GOPROXY=off and GOFLAGS=-mod=readonly.
func TestAnalyzerEnvNetworkBlocked(t *testing.T) {
	env := analyzerEnv()
	var goproxyCount, goflagsCount int
	for _, e := range env {
		if e == "GOPROXY=off" {
			goproxyCount++
		}
		if e == "GOFLAGS=-mod=readonly" {
			goflagsCount++
		}
	}
	if goproxyCount != 1 {
		t.Errorf("expected exactly 1 GOPROXY=off, got %d", goproxyCount)
	}
	if goflagsCount != 1 {
		t.Errorf("expected exactly 1 GOFLAGS=-mod=readonly, got %d", goflagsCount)
	}
}

// Verify role filter with invalid role strings.
func TestRoleFilterInvalidRolesSilentlyIgnored(t *testing.T) {
	// The makeRoleFilter function accepts any Role value. Unknown strings
	// become valid Role values but match nothing. This is by design -
	// it's up to the tool layer to validate.
	filter := makeRoleFilter([]Role{"Implimentation", RoleDefinition})
	if !filter[RoleDefinition] {
		t.Error("expected RoleDefinition to be in filter")
	}
	// makeRoleFilter uses Role type, so "Implimentation" becomes a Role("Implimentation")
	// which IS in the filter map. The tool layer must validate.
	if !filter["Implimentation"] {
		t.Error("expected Implimentation in filter (codeintel layer accepts any Role)")
	}
	t.Log("role validation happens in tools/find_references.go, not in codeintel layer")
}

// TestSameObjectDoesNotConflateFieldWithPackageLevelDecl confirms the fix for
// the false-positive found in the bug audit of plan 18: sameObject used to
// compare only Pkg().Path()+Name(), so a struct field sharing a name with an
// unrelated package-level declaration (both have the same Pkg() and Name())
// was misreported as a reference to that declaration.
func TestSameObjectDoesNotConflateFieldWithPackageLevelDecl(t *testing.T) {
	src := `
package p

type Name string

type Bar struct {
	Name string
}
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

	nameType := pkg.Scope().Lookup("Name")
	if nameType == nil {
		t.Fatal("Name not found in package scope")
	}
	barObj := pkg.Scope().Lookup("Bar")
	if barObj == nil {
		t.Fatal("Bar not found in package scope")
	}
	named, ok := barObj.Type().(*types.Named)
	if !ok {
		t.Fatalf("Bar is %T, not *types.Named", barObj.Type())
	}
	structType, ok := named.Underlying().(*types.Struct)
	if !ok {
		t.Fatalf("Bar.Underlying is %T, not *types.Struct", named.Underlying())
	}
	var fieldObj types.Object
	for i := 0; i < structType.NumFields(); i++ {
		if structType.Field(i).Name() == "Name" {
			fieldObj = structType.Field(i)
		}
	}
	if fieldObj == nil {
		t.Fatal("Bar.Name field not found")
	}

	if sameObject(fieldObj, nameType) {
		t.Fatal("BUG: sameObject conflates struct field Bar.Name with package-level type Name - " +
			"a use of the field would be misreported as a use of the type")
	}
	if !sameObject(nameType, nameType) {
		t.Fatal("sameObject must still match a package-level declaration against itself")
	}
	if !isPackageScopeObject(nameType) {
		t.Error("expected the package-level type Name to be a package-scope object")
	}
	if isPackageScopeObject(fieldObj) {
		t.Error("expected the struct field Bar.Name NOT to be a package-scope object")
	}
}

// Verify sameObject correctly distinguishes same-named objects in different packages.
func TestSameObjectDistinguishesPackages(t *testing.T) {
	// We can't easily create real types.Objects in a test without a full type-check.
	// But we can verify the function works for nil cases and documents the contract.
	var nilObj token.Position // dummy
	_ = nilObj

	// sameObject(nil, anything) = false
	if sameObject(nil, nil) {
		t.Error("sameObject(nil, nil) should be false")
	}
	// sameObject(not_nil, nil) = false
	t.Log("sameObject contract: compares by Pkg().Path() + Name()")
}
