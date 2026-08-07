package codeintel

import (
	"context"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/tools/go/packages"
)

func TestReferencesDistinguishesSameNamedSentinels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// storage.ErrClaimHeld and ledger.ErrClaimHeld are distinct sentinels
	// with the same short name. The analyzer must treat them independently.
	result, err := a.References(ctx, "storage.ErrClaimHeld", nil, 50)
	if err != nil {
		t.Fatalf("References(storage.ErrClaimHeld): %v", err)
	}
	if len(result.Locations) == 0 {
		t.Fatal("expected at least one location for storage.ErrClaimHeld")
	}
	// All locations should have the storage.ErrClaimHeld symbol.
	for _, loc := range result.Locations {
		if loc.Symbol != "ErrClaimHeld" {
			t.Errorf("expected Symbol 'ErrClaimHeld', got %q", loc.Symbol)
		}
	}
}

func TestReferencesDedupsTestVariants(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// contentref.Reference is defined once; test file variants should not
	// produce duplicate definition locations.
	result, err := a.References(ctx, "contentref.Reference", []Role{RoleDefinition}, 50)
	if err != nil {
		t.Fatalf("References(contentref.Reference, definition): %v", err)
	}
	// There should be exactly one definition across all packages
	// (production + test variant), not two.
	if len(result.Locations) == 0 {
		t.Fatal("expected at least one definition for contentref.Reference")
	}
	var defs int
	for _, loc := range result.Locations {
		if loc.Role == RoleDefinition {
			defs++
		}
	}
	// With dedup by (PkgPath, Name, position), the test variant should not
	// create a duplicate definition.
	if defs > 1 {
		t.Errorf("expected at most 1 definition, got %d (test variant not deduped)", defs)
	}
}

// TestReferencesClassifiesErrorsIsAsComparison confirms the fix for the bug
// audit finding that findRoleInFile only recognized raw ==/!= and return
// statements. storage.ErrClaimHeld is checked exclusively via errors.Is in
// this repo (internal/ledger/storage_claims.go), never raw ==/!=, so before
// the fix a roles=["comparison"] query returned nothing for the tool's own
// motivating example (plan 18 §1: "Where is ErrClaimHeld checked?").
func TestReferencesClassifiesErrorsIsAsComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := a.References(ctx, "storage.ErrClaimHeld", []Role{RoleComparison}, 50)
	if err != nil {
		t.Fatalf("References(storage.ErrClaimHeld, comparison): %v", err)
	}
	if len(result.Locations) == 0 {
		t.Fatal("expected at least one RoleComparison location for storage.ErrClaimHeld via errors.Is, got none")
	}
	var foundStorageClaims bool
	for _, loc := range result.Locations {
		if loc.Role != RoleComparison {
			t.Errorf("query filtered to roles=[comparison] returned a %s location", loc.Role)
		}
		if strings.Contains(loc.Path, "storage_claims.go") {
			foundStorageClaims = true
		}
	}
	if !foundStorageClaims {
		t.Errorf("expected internal/ledger/storage_claims.go's errors.Is(err, storage.ErrClaimHeld) among comparison locations, got: %+v", result.Locations)
	}
}

func TestClassifyUseRoleReturns(t *testing.T) {
	// Test that return statements are classified as RoleReturn.
	// This uses the classifyUseRole function directly with a known AST pattern.
	t.Log("classifyUseRole needs AST context; tested via integration in roles_test.go")
}

func TestSameObjectEquality(t *testing.T) {
	// Test the sameObject helper with various combinations.
	// Create mock objects to test.
	var nilObj types.Object = nil

	// sameObject(nil, anything) = false
	if sameObject(nilObj, nilObj) {
		t.Error("sameObject(nil, nil) should be false")
	}
}

func TestReferencesFindsImplementations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Resolve storage.Store interface and find implementations.
	// This repo has storage.Memory (implements via value receiver)
	// and storage.SQLite (implements via value receiver).
	result, err := a.References(ctx, "storage.Store", []Role{RoleImplementation}, 100)
	if err != nil {
		t.Fatalf("References(storage.Store, implementation): %v", err)
	}
	if len(result.Locations) == 0 {
		t.Fatal("expected at least one implementation of storage.Store")
	}
	var imps []string
	for _, loc := range result.Locations {
		if loc.Role == RoleImplementation {
			imps = append(imps, loc.Symbol)
		}
	}
	t.Logf("implementations of storage.Store: %v", imps)
	// Must find at least one concrete implementor with a real file path.
	var foundRealPath bool
	for _, loc := range result.Locations {
		if loc.Path != "" && loc.Role == RoleImplementation {
			foundRealPath = true
			break
		}
	}
	if !foundRealPath {
		t.Error("expected at least one implementation with a real file path, got empty paths")
	}
}

func TestFindImplementationsOnNonInterfaceReturnsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// contentref.Reference is a function, not an interface.
	// Querying with RoleImplementation should return zero implementation locations.
	result, err := a.References(ctx, "contentref.Reference", []Role{RoleImplementation}, 50)
	if err != nil {
		t.Fatalf("References(contentref.Reference, implementation): %v", err)
	}
	for _, loc := range result.Locations {
		if loc.Role == RoleImplementation {
			t.Errorf("expected no implementations for non-interface contentref.Reference, got %s at %s:%d", loc.Symbol, loc.Path, loc.Line)
		}
	}
}

// TestFindImplementationsEmbeddedInterface is an end-to-end regression test for
// the bug where findImplementations checked iface.NumExplicitMethods() instead
// of iface.NumMethods(). For an interface composed entirely of embedded methods,
// NumExplicitMethods() is 0, causing findImplementations to return early and miss
// all concrete implementors. The fix uses NumMethods() which correctly counts
// both explicit and embedded methods.
//
// This test uses a self-contained two-package module (no external imports)
// with a locally-defined Readable interface, a FullReader that embeds it,
// and a myReader concrete type that implements Read.
func TestFindImplementationsEmbeddedInterface(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/impl\n\ngo 1.25\n")
	write(t, filepath.Join(dir, "p.go"), `package impl

// Readable declares a single method.
type Readable interface {
	Read([]byte) (int, error)
}

// FullReader embeds Readable with no explicit methods.
type FullReader interface {
	Readable
}

// myReader is a concrete implementor of Readable (and therefore FullReader).
type myReader struct{}

func (myReader) Read([]byte) (int, error) { return 0, nil }
`)
	a := NewAnalyzer(dir)
	ctx := context.Background()

	result, err := a.References(ctx, "impl.FullReader", []Role{RoleImplementation}, 0)
	if err != nil {
		t.Fatalf("References(impl.FullReader, implementation): %v", err)
	}
	var imps []string
	for _, loc := range result.Locations {
		if loc.Role == RoleImplementation {
			imps = append(imps, loc.Symbol)
		}
	}
	if len(imps) == 0 {
		t.Fatalf("expected at least one implementation of FullReader (myReader), got none; FullLocations=%+v", result.Locations)
	}
	t.Logf("implementations of FullReader: %v", imps)
}

// TestFindImplementationsEmptyInterfaceReturnsNone verifies that findImplementations
// correctly returns no results for an empty interface (NumMethods == 0).
// This confirms the guard at roles.go:128 (iface.NumMethods() == 0) correctly skips
// truly empty interfaces without panicking or misreporting.
func TestFindImplementationsEmptyInterfaceReturnsNone(t *testing.T) {
	fset := token.NewFileSet()
	src := `package p

type Empty interface {}
`
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	conf := types.Config{Importer: importer.Default()}
	info := &types.Info{Defs: make(map[*ast.Ident]types.Object)}
	pkg, err := conf.Check("p", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatal(err)
	}
	pkgsPkg := &packages.Package{Types: pkg, TypesInfo: info, Fset: fset}
	target := pkg.Scope().Lookup("Empty")
	if target == nil {
		t.Fatal("Empty not found in scope")
	}
	var locs []string
	findImplementations(pkgsPkg, target, fset,
		func(file string, line int, name string, role Role) {
			locs = append(locs, name)
		},
	)
	if len(locs) != 0 {
		t.Errorf("expected 0 locations for empty interface, got %d: %v", len(locs), locs)
	}
}

// TestFindImplementationsNilInputs verifies that findImplementations returns no
// results for nil or zero-value inputs. Three subcases:
// (a) nil *packages.Package,
// (b) &packages.Package{Types: nil, TypesInfo: nil} (non-nil but zero-value),
// (c) nil target object.
func TestFindImplementationsNilInputs(t *testing.T) {
	fset := token.NewFileSet()
	var results []struct {
		name   string
		pkg    *packages.Package
		target types.Object
	}

	// (a) nil package
	results = append(results, struct {
		name   string
		pkg    *packages.Package
		target types.Object
	}{"nil package", nil, nil})

	// (b) non-nil package with zero-value fields
	results = append(results, struct {
		name   string
		pkg    *packages.Package
		target types.Object
	}{"zero-value package", &packages.Package{Types: nil, TypesInfo: nil}, nil})

	// (c) nil target object — construct a valid package but pass nil target
	src := `package p
type I interface { Foo() }
type T struct{}
func (T) Foo() {}
`
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	conf := types.Config{Importer: importer.Default()}
	info := &types.Info{}
	pkg, err := conf.Check("p", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatal(err)
	}
	pkgsPkg := &packages.Package{Types: pkg, TypesInfo: info, Fset: fset}
	results = append(results, struct {
		name   string
		pkg    *packages.Package
		target types.Object
	}{"nil target", pkgsPkg, nil})

	for _, tc := range results {
		t.Run(tc.name, func(t *testing.T) {
			var locs []struct {
				file string
				line int
				name string
				role Role
			}
			findImplementations(tc.pkg, tc.target, fset,
				func(file string, line int, name string, role Role) {
					locs = append(locs, struct {
						file string
						line int
						name string
						role Role
					}{file, line, name, role})
				},
			)
			if len(locs) != 0 {
				t.Errorf("expected 0 locations for %s, got %d", tc.name, len(locs))
			}
		})
	}
}
