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
