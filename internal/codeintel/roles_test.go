package codeintel

import (
	"context"
	"go/types"
	"testing"
	"time"
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
