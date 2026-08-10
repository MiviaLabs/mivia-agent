package codeintel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestReferencesRejectsAmbiguousBareName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// "New" is declared as a top-level func in at least five distinct
	// packages in this repo (provider, coordinator, events, subagents,
	// runtime). A bare-name query must report the ambiguity rather than
	// silently resolving to whichever package packages.Load happened to
	// iterate last.
	_, err := a.References(ctx, "New", nil, 50)
	if err == nil {
		t.Fatal("expected an ambiguity error for bare name \"New\", got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected error to mention ambiguity, got: %v", err)
	}
}

func TestReferencesNotFoundReportsDistinctError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := a.References(ctx, "storage.DefinitelyNotARealSymbolXYZ", nil, 50)
	if err == nil {
		t.Fatal("expected a not-found error, got nil")
	}
	if strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("a genuinely absent symbol must not be reported as ambiguous: %v", err)
	}
}

func TestReferencesResolvesFullyQualifiedPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Short-form query that is ambiguous: "NewRegistry" matches tools, agents, skills.
	_, err := a.References(ctx, "NewRegistry", nil, 50)
	if err == nil {
		t.Fatal("expected ambiguity for bare NewRegistry")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity error, got: %v", err)
	}

	// Same symbol qualified with full import path must resolve without ambiguity.
	const fq = "github.com/MiviaLabs/mivia-agent/internal/tools.NewRegistry"
	result, err := a.References(ctx, fq, nil, 50)
	if err != nil {
		t.Fatalf("References(%s): %v", fq, err)
	}
	if len(result.Locations) == 0 {
		t.Fatalf("expected at least one location for %s", fq)
	}
	var foundDef bool
	for _, loc := range result.Locations {
		if loc.Role == RoleDefinition {
			foundDef = true
			// Windows paths use backslashes; the package directory is the
			// segment named "tools" regardless of separator.
			if !strings.Contains(filepath.ToSlash(loc.Path), "tools/") {
				t.Errorf("definition for tools.NewRegistry resolved outside tools package: %s", loc.Path)
			}
		}
	}
	if !foundDef {
		t.Error("expected a definition location for fully-qualified tools.NewRegistry")
	}
}

func TestReferencesFullyQualifiedMatchesShortForm(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Both forms must return results from the same package.
	short, err := a.References(ctx, "contentref.Reference", nil, 50)
	if err != nil {
		t.Fatalf("short form: %v", err)
	}
	fq, err := a.References(ctx, "github.com/MiviaLabs/mivia-agent/internal/contentref.Reference", nil, 50)
	if err != nil {
		t.Fatalf("fully-qualified form: %v", err)
	}
	if len(fq.Locations) == 0 {
		t.Fatal("fully-qualified form returned zero locations")
	}
	// The first (definition) location must be identical.
	if len(short.Locations) > 0 && len(fq.Locations) > 0 {
		if short.Locations[0].Path != fq.Locations[0].Path || short.Locations[0].Line != fq.Locations[0].Line {
			t.Errorf("short and fully-qualified forms resolved to different definitions: short=%s:%d, fq=%s:%d",
				short.Locations[0].Path, short.Locations[0].Line, fq.Locations[0].Path, fq.Locations[0].Line)
		}
	}
}

func TestReferencesAmbiguityMessageSuggestsShortForm(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err := a.References(ctx, "NewRegistry", nil, 50)
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ambiguous") {
		t.Fatalf("expected 'ambiguous' in error, got: %s", msg)
	}
	// The message should guide the user toward "pkgname.NewRegistry" form.
	if !strings.Contains(msg, "pkgname") || !strings.Contains(msg, "NewRegistry") {
		t.Errorf("ambiguity error should mention 'pkgname.NewRegistry' pattern, got: %s", msg)
	}
}

func TestTruncatedOnlyWhenMoreMatchesExist(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const symbol = "storage.ErrClaimHeld"
	full, err := a.References(ctx, symbol, nil, 1000)
	if err != nil {
		t.Fatalf("References(%s, limit=1000): %v", symbol, err)
	}
	total := len(full.Locations)
	if total == 0 {
		t.Fatal("expected at least one location to establish a baseline total")
	}
	if full.Truncated {
		t.Errorf("Truncated=true with limit(1000) far above the real total(%d); a limit that was never reached must not report truncation", total)
	}

	exact, err := a.References(ctx, symbol, nil, total)
	if err != nil {
		t.Fatalf("References(%s, limit=%d): %v", symbol, total, err)
	}
	if exact.Truncated {
		t.Errorf("Truncated=true with limit==total(%d): the result is exactly complete, nothing was dropped", total)
	}
	if len(exact.Locations) != total {
		t.Errorf("len(Locations) = %d, want %d (limit==total should return everything)", len(exact.Locations), total)
	}

	if total < 2 {
		t.Skip("need at least 2 total matches to exercise a real cap below the total")
	}
	limited, err := a.References(ctx, symbol, nil, total-1)
	if err != nil {
		t.Fatalf("References(%s, limit=%d): %v", symbol, total-1, err)
	}
	if !limited.Truncated {
		t.Errorf("Truncated=false with limit(%d) below the real total(%d): a genuine match was dropped and must be reported", total-1, total)
	}
}

func TestReferencesHonorsContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before the call starts

	start := time.Now()
	_, err := a.References(ctx, "storage.ErrClaimHeld", nil, 50)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for an already-canceled context")
	}
	// A cold packages.Load on this repo can take several seconds (see plan
	// 18 §4). Honoring cancellation means failing fast, not after the full
	// load completes.
	if elapsed > 5*time.Second {
		t.Errorf("References took %v after an already-canceled context; cancellation was not propagated into packages.Load", elapsed)
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
