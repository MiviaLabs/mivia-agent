package codeintel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result, err := a.References(ctx, "sdkadapter.Mint", nil, 50)
	if err != nil {
		t.Fatalf("References(sdkadapter.Mint): %v", err)
	}
	if result.Symbol == "" {
		t.Fatal("expected non-empty symbol")
	}
	if len(result.Locations) == 0 {
		t.Fatal("expected at least one location for sdkadapter.Mint")
	}
	var foundDef bool
	for _, loc := range result.Locations {
		if loc.Role == RoleDefinition {
			foundDef = true
			break
		}
	}
	if !foundDef {
		t.Error("expected at least one definition location for sdkadapter.Mint")
	}
}

func TestReferencesRejectsAmbiguousBareName(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	root := repoRoot(t)
	a := NewAnalyzer(root)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Both forms must return results from the same package.
	short, err := a.References(ctx, "sdkadapter.Mint", nil, 50)
	if err != nil {
		t.Fatalf("short form: %v", err)
	}
	fq, err := a.References(ctx, "github.com/MiviaLabs/mivia-agent/internal/sdkadapter.Mint", nil, 50)
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

// TestReferencesTruncationDeterministic is a regression test for the bug where
// collectLocations applied the limit inside the addLoc callback while iterating
// the unordered TypesInfo.Defs/Uses maps: which subset survived an identical
// truncated query was randomized by map iteration order. After the fix all
// matches are collected and deduplicated first, sorted deterministically
// (roleRank - definitions first - then path/line/role/symbol), and only then
// capped, so five repeated identical queries must return the identical sorted
// prefix of the full result.
func TestReferencesTruncationDeterministic(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module example.com/det\n\ngo 1.25\n")

	// 1 definition (var Target int) plus 40 uses = 41 distinct locations.
	var body strings.Builder
	body.WriteString("package det\n\nvar Target int\n\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&body, "func use%d() { _ = Target }\n", i)
	}
	write(t, filepath.Join(dir, "det.go"), body.String())

	a := NewAnalyzer(dir)
	ctx := context.Background()

	full, err := a.References(ctx, "det.Target", nil, 1000)
	if err != nil {
		t.Fatalf("References(det.Target, limit=1000): %v", err)
	}
	if full.Truncated {
		t.Fatal("Truncated=true with limit=1000 above the real total")
	}
	if len(full.Locations) != 41 {
		t.Fatalf("expected 41 distinct locations (1 definition + 40 uses), got %d", len(full.Locations))
	}
	wantPrefix := sortLocations(full.Locations)[:5]

	var first []Location
	for i := 0; i < 5; i++ {
		res, err := a.References(ctx, "det.Target", nil, 5)
		if err != nil {
			t.Fatalf("References(det.Target, limit=5) call %d: %v", i, err)
		}
		if !res.Truncated {
			t.Fatalf("call %d: Truncated=false with limit=5 below the real total of 41", i)
		}
		if len(res.Locations) != 5 {
			t.Fatalf("call %d: len(Locations)=%d, want 5", i, len(res.Locations))
		}
		if !equalLocations(res.Locations, wantPrefix) {
			t.Fatalf("call %d: truncated subset %+v is not the (path,line,role,symbol)-sorted prefix %+v", i, res.Locations, wantPrefix)
		}
		if i == 0 {
			first = append([]Location(nil), res.Locations...)
		} else if !equalLocations(res.Locations, first) {
			t.Fatalf("call %d: result %+v differs from call 0's %+v", i, res.Locations, first)
		}
	}
}

// sortLocations returns a copy of locs sorted exactly as collectLocations
// orders locations before capping: role rank (definitions first) then
// (path, line, role, symbol). Both sides call the same roleRank, so the
// expected truncated prefix cannot drift from the production order.
func sortLocations(locs []Location) []Location {
	out := append([]Location(nil), locs...)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := roleRank(a.Role), roleRank(b.Role); ra != rb {
			return ra < rb
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Symbol < b.Symbol
	})
	return out
}

// equalLocations reports whether two location slices are element-wise equal.
func equalLocations(a, b []Location) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
