package memory

import (
	"context"
	"errors"
	"testing"
)

// TestStoreDeleteRemovesEntry pins the core Delete contract on both backends:
// deleting a saved entry makes it disappear from search and count, and the
// entry's id is no longer resolvable.
func TestStoreDeleteRemovesEntry(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "github.com/acme")
			ctx := context.Background()
			proj, err := s.Save(ctx, testEntry("delete-me", ScopeProject))
			if err != nil {
				t.Fatalf("save project: %v", err)
			}
			org, err := s.Save(ctx, testEntry("delete-org", ScopeOrg))
			if err != nil {
				t.Fatalf("save org: %v", err)
			}

			if err := s.Delete(ctx, proj.ID); err != nil {
				t.Fatalf("delete project entry: %v", err)
			}
			// The deleted project entry must no longer match a search.
			projHits, err := s.Search(ctx, Query{Text: "delete-me", Scope: ScopeProject})
			if err != nil {
				t.Fatalf("search after delete: %v", err)
			}
			if len(projHits) != 0 {
				t.Errorf("project search after delete = %d hits, want 0", len(projHits))
			}
			if n, _ := s.Count(ctx, ScopeProject); n != 0 {
				t.Errorf("project count after delete = %d, want 0", n)
			}

			// The org entry must be untouched.
			orgHits, err := s.Search(ctx, Query{Text: "delete-org", Scope: ScopeOrg})
			if err != nil {
				t.Fatalf("org search: %v", err)
			}
			if len(orgHits) != 1 {
				t.Errorf("org search after project delete = %d hits, want 1", len(orgHits))
			}

			// Deleting the org entry removes it too.
			if err := s.Delete(ctx, org.ID); err != nil {
				t.Fatalf("delete org entry: %v", err)
			}
			if n, _ := s.Count(ctx, ScopeOrg); n != 0 {
				t.Errorf("org count after delete = %d, want 0", n)
			}
		})
	}
}

// TestStoreDeleteUnknownID pins that deleting an id that exists in no store
// returns ErrEntryNotFound, matching PromoteToCore's contract.
func TestStoreDeleteUnknownID(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "github.com/acme")
			err := s.Delete(context.Background(), "does-not-exist")
			if !errors.Is(err, ErrEntryNotFound) {
				t.Fatalf("Delete unknown id error = %v, want ErrEntryNotFound", err)
			}
		})
	}
}

// TestStoreDeleteIsIdempotentForAbsentEntry pins that deleting an already
// deleted id returns ErrEntryNotFound (not a nil), so a caller can tell a
// successful delete from a no-op.
func TestStoreDeleteIsIdempotentForAbsentEntry(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			saved, err := s.Save(ctx, testEntry("gone", ScopeProject))
			if err != nil {
				t.Fatalf("save: %v", err)
			}
			if err := s.Delete(ctx, saved.ID); err != nil {
				t.Fatalf("first delete: %v", err)
			}
			if err := s.Delete(ctx, saved.ID); !errors.Is(err, ErrEntryNotFound) {
				t.Fatalf("second delete error = %v, want ErrEntryNotFound", err)
			}
		})
	}
}

// TestStoreDeleteRefusesReadOnly pins that a read-only store refuses Delete
// on both backends, matching Save and PromoteToCore.
func TestStoreDeleteRefusesReadOnly(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			cfg := Config{
				Backend:          backend,
				ProjectPath:      t.TempDir() + "/project.db",
				MaxEntryBytes:    8192,
				MaxEntries:       5,
				MaxSearchResults: 8,
				ReadOnly:         true,
			}
			s, err := Open(cfg)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer s.Close()
			err = s.Delete(context.Background(), "any-id")
			if err == nil {
				t.Fatal("Delete on a read-only store must fail")
			}
		})
	}
}
