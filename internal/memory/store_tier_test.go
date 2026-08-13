package memory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// newTierTestStore mirrors newTestStore but with MaxEntries large enough to
// save past CoreTierCap - newTestStore's MaxEntries: 5 is too small to prove
// the cap is enforced independently of the store's overall row cap.
func newTierTestStore(t *testing.T, backend, orgID string) Store {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		Backend:          backend,
		ProjectPath:      filepath.Join(dir, "project.db"),
		OrgPath:          filepath.Join(dir, "org.db"),
		OrgID:            orgID,
		MaxEntryBytes:    8192,
		MaxEntries:       CoreTierCap + 10,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPromoteToCoreEnforcesCap(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTierTestStore(t, backend, "")
			ctx := context.Background()

			var ids []string
			for i := 0; i < CoreTierCap+1; i++ {
				res, err := s.Save(ctx, testEntry(fmt.Sprintf("cap-%02d", i), ScopeProject))
				if err != nil {
					t.Fatalf("save %d: %v", i, err)
				}
				ids = append(ids, res.ID)
			}

			for i := 0; i < CoreTierCap; i++ {
				if err := s.PromoteToCore(ctx, ids[i]); err != nil {
					t.Fatalf("promote %d: %v", i, err)
				}
			}

			// The 25th promotion must fail with a named error, never a
			// silent eviction of an existing core entry.
			err := s.PromoteToCore(ctx, ids[CoreTierCap])
			if !errors.Is(err, ErrCoreTierFull) {
				t.Fatalf("promote past cap: got %v, want ErrCoreTierFull", err)
			}

			// Re-promoting an already-core entry is a no-op, not an error.
			if err := s.PromoteToCore(ctx, ids[0]); err != nil {
				t.Fatalf("re-promote already-core entry: %v", err)
			}
		})
	}
}

func TestPromoteToCoreUnknownID(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			err := s.PromoteToCore(context.Background(), "does-not-exist")
			if !errors.Is(err, ErrEntryNotFound) {
				t.Fatalf("promote unknown id: got %v, want ErrEntryNotFound", err)
			}
		})
	}
}

func TestSaveNeverSetsCoreTier(t *testing.T) {
	// Structural confirmation of D1a: Save's INSERT statement never names
	// the tier column, so every new row lands at the schema default
	// ("archive") regardless of anything the caller passes in Entry - Entry
	// itself has no field that could carry a tier value (see
	// internal/tools/memory_tier_guard_test.go for the write-path proxy).
	s := newTestStore(t, "sqlite", "")
	ctx := context.Background()
	res, err := s.Save(ctx, testEntry("archive-by-default", ScopeProject))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	// A second promotion attempt on a fresh, never-promoted id must succeed
	// (proves it started as "archive", not already "core").
	if err := s.PromoteToCore(ctx, res.ID); err != nil {
		t.Fatalf("promote freshly-saved entry: %v", err)
	}
}
