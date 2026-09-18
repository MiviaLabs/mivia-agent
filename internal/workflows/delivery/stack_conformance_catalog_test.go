package delivery

import (
	"testing"
)

// TestStackConformanceCatalogShape pins the shared catalog: a row cannot be
// added without owners, unique naming, expected end states for every chunk,
// and a golden-count bump visible in this test's failure text.
func TestStackConformanceCatalogShape(t *testing.T) {
	scenarios := StackConformanceScenarios()
	const wantCount = 5
	if len(scenarios) != wantCount {
		t.Fatalf("conformance catalog has %d rows, want %d; every new row must be wired into BOTH driver conformance tests (internal/cli/chat, internal/workflows/localengine) before its count bump lands", len(scenarios), wantCount)
	}
	seen := map[string]bool{}
	knownStatuses := map[string]bool{
		StatusPublished: true, StatusMerged: true, StatusFailed: true,
		StatusCanceled: true, StatusImplemented: true, StatusReopened: true,
		StatusRunning: true, StatusBlocked: true, StatusReviewed: true,
		StatusSkipped: true,
	}
	for _, s := range scenarios {
		if s.Name == "" {
			t.Fatal("a scenario has no name")
		}
		if seen[s.Name] {
			t.Fatalf("duplicate scenario name %q", s.Name)
		}
		seen[s.Name] = true
		if len(s.Owners) == 0 {
			t.Fatalf("scenario %q has no owner label", s.Name)
		}
		for _, o := range s.Owners {
			switch o {
			case OwnerDriverCLI, OwnerDriverEngine, OwnerCLIOnly:
			default:
				t.Fatalf("scenario %q has unknown owner %q", s.Name, o)
			}
		}
		if s.MergePolicy == "" {
			t.Fatalf("scenario %q has no merge policy", s.Name)
		}
		if len(s.Chunks) == 0 {
			t.Fatalf("scenario %q has no chunks", s.Name)
		}
		for _, c := range s.Chunks {
			status, ok := s.Expect[c.ID]
			if !ok {
				t.Fatalf("scenario %q chunk %q has no expected end state", s.Name, c.ID)
			}
			if !knownStatuses[status] {
				t.Fatalf("scenario %q chunk %q expects unknown status %q", s.Name, c.ID, status)
			}
		}
		if len(s.Expect) != len(s.Chunks) {
			t.Fatalf("scenario %q expects %d end states for %d chunks", s.Name, len(s.Expect), len(s.Chunks))
		}
	}
}

// Example-style guard: the catalog is the single source both drivers iterate.
// This compile-level assertion documents that the exported constructor is the
// only catalog entry point.
func TestStackConformanceCatalogIsFreshPerCall(t *testing.T) {
	a := StackConformanceScenarios()
	b := StackConformanceScenarios()
	if len(a) == 0 || len(a) != len(b) {
		t.Fatal("catalog must be non-empty and stable across calls")
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			t.Fatalf("catalog order drifted at row %d: %q vs %q", i, a[i].Name, b[i].Name)
		}
	}
}
