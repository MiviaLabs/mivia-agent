package memory

import (
	"context"
	"fmt"
	"testing"
)

func TestCoreEntriesReturnsOnlyCoreTierUpToCap(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTierTestStore(t, backend, "")
			ctx := context.Background()

			var coreIDs []string
			for i := 0; i < CoreTierCap+3; i++ {
				res, err := s.Save(ctx, testEntry(fmt.Sprintf("core-%02d", i), ScopeProject))
				if err != nil {
					t.Fatalf("save %d: %v", i, err)
				}
				if i < CoreTierCap {
					if err := s.PromoteToCore(ctx, res.ID); err != nil {
						t.Fatalf("promote %d: %v", i, err)
					}
					coreIDs = append(coreIDs, res.ID)
				}
			}

			entries, err := s.CoreEntries(ctx, ScopeProject)
			if err != nil {
				t.Fatalf("CoreEntries: %v", err)
			}
			if len(entries) != CoreTierCap {
				t.Fatalf("CoreEntries returned %d entries, want %d (the cap, and no archive rows)", len(entries), CoreTierCap)
			}
			seen := map[string]bool{}
			for _, e := range entries {
				seen[e.ID] = true
			}
			for _, id := range coreIDs {
				if !seen[id] {
					t.Errorf("promoted id %q missing from CoreEntries", id)
				}
			}

			// Deterministic: two calls with no writes in between return the
			// same order (prefix-cache friendliness, D1a's design goal).
			again, err := s.CoreEntries(ctx, ScopeProject)
			if err != nil {
				t.Fatalf("CoreEntries (again): %v", err)
			}
			if len(again) != len(entries) {
				t.Fatalf("CoreEntries not stable across calls: %d vs %d", len(again), len(entries))
			}
			for i := range entries {
				if entries[i].ID != again[i].ID {
					t.Fatalf("CoreEntries order not stable at index %d: %q vs %q", i, entries[i].ID, again[i].ID)
				}
			}
		})
	}
}

// TestSearchReturnsBothTiersUnaffected is the mixed-tier Search() coverage
// the Step 5+ review found missing: promoting an entry to core must not
// remove it from, or otherwise break, ordinary text search - tier is a
// separate concern from search ranking (D4's ranking stays keyed on
// `created`/text rank, not tier).
func TestSearchReturnsBothTiersUnaffected(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()

			resCore, err := s.Save(ctx, testEntry("promoted deploy fact", ScopeProject))
			if err != nil {
				t.Fatalf("save core candidate: %v", err)
			}
			if err := s.PromoteToCore(ctx, resCore.ID); err != nil {
				t.Fatalf("promote: %v", err)
			}
			resArchive, err := s.Save(ctx, testEntry("archived deploy fact", ScopeProject))
			if err != nil {
				t.Fatalf("save archive entry: %v", err)
			}

			results, err := s.Search(ctx, Query{Text: "deploy", Scope: ScopeProject, MaxResults: 10})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			seen := map[string]bool{}
			for _, r := range results {
				seen[r.ID] = true
			}
			if !seen[resCore.ID] {
				t.Errorf("promoted (core) entry %q missing from search results: %+v", resCore.ID, results)
			}
			if !seen[resArchive.ID] {
				t.Errorf("archive entry %q missing from search results: %+v", resArchive.ID, results)
			}
			if len(results) != 2 {
				t.Fatalf("search returned %d results, want exactly 2 (both tiers)", len(results))
			}
		})
	}
}

func TestCoreEntriesEmptyWhenNoneCore(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			if _, err := s.Save(ctx, testEntry("archive-only", ScopeProject)); err != nil {
				t.Fatalf("save: %v", err)
			}
			entries, err := s.CoreEntries(ctx, ScopeProject)
			if err != nil {
				t.Fatalf("CoreEntries: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("CoreEntries = %d entries, want 0", len(entries))
			}
		})
	}
}

// TestCoreEntriesScopeAllErrors is the plan-77 (E4) regression for a
// footgun found during Step 0 investigation: ScopeAll is not a valid
// CoreEntries argument - dbFor would otherwise silently route anything but
// ScopeOrg to the project DB, and no entry can ever have scope "all"
// (rejected by Validate). The original plan text expected this to return
// zero rows silently; while implementing this test against BOTH backends
// found the in-memory backend already validates and errors loudly
// (matching Search's scope-validation shape) - the sqlite backend was
// fixed to match for parity, since a loud error is strictly safer than a
// silent empty result for a caller-side mistake.
func TestCoreEntriesScopeAllErrors(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			res, err := s.Save(ctx, testEntry("promoted fact", ScopeProject))
			if err != nil {
				t.Fatalf("save: %v", err)
			}
			if err := s.PromoteToCore(ctx, res.ID); err != nil {
				t.Fatalf("promote: %v", err)
			}
			_, err = s.CoreEntries(ctx, ScopeAll)
			if err == nil {
				t.Fatalf("CoreEntries(ScopeAll) succeeded, want an error (ScopeAll is invalid - use ScopeProject/ScopeOrg)")
			}
		})
	}
}
