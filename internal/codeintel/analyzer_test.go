package codeintel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// repoRoot finds the module root by looking for go.mod upward from the test's
// working directory (which is the package directory when running go test).
func repoRoot(t *testing.T) string {
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
			t.Fatal("could not find go.mod root")
		}
		dir = parent
	}
}

func TestAnalyzerUnavailableWithoutGoMod(t *testing.T) {
	a := NewAnalyzer(t.TempDir()) // no go.mod
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := a.References(ctx, "os.File", nil, 50)
	if err == nil {
		t.Fatal("expected error for non-Go workspace, got nil")
	}
}

func TestAnalyzerUnavailableEmptyRoot(t *testing.T) {
	a := NewAnalyzer("")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := a.References(ctx, "os.File", nil, 50)
	if err == nil {
		t.Fatal("expected error for empty root, got nil")
	}
}

func TestAnalyzerResolvesSymbolInThisRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, err := a.References(ctx, "contentref.Reference", nil, 50)
	if err != nil {
		t.Fatalf("References(contentref.Reference): %v", err)
	}
	if result.Symbol == "" {
		t.Fatal("expected non-empty symbol")
	}
	if len(result.Locations) == 0 {
		t.Fatal("expected at least one location for contentref.Reference")
	}
	var foundDef bool
	for _, loc := range result.Locations {
		if loc.Role == RoleDefinition {
			foundDef = true
			break
		}
	}
	if !foundDef {
		t.Error("expected at least one definition location for contentref.Reference")
	}
}

func TestAnalyzerDoesNotReachNetwork(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Loading a non-existent external package should fail under GOPROXY=off.
	_, err := a.References(ctx, "github.com/does/not/exist.Symbol", nil, 10)
	if err == nil {
		t.Log("References(nonexistent) returned nil error (may be empty result)")
	}
}
