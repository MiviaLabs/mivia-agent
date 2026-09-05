package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncMemoryIndexReplacesDeletedSourcesAndPreservesTier(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	docs := []MemoryIndexDocument{
		{ID: "one", Scope: "project", ProjectID: "repo-a", SourcePath: "/repo/one.md", SourceHash: "h1", Title: "One", Summary: "first", Verdict: "good", Created: "2026-09-04", Content: "one", Tier: "archive"},
		{ID: "two", Scope: "project", ProjectID: "repo-a", SourcePath: "/repo/two.md", SourceHash: "h2", Title: "Two", Summary: "second", Verdict: "neutral", Created: "2026-09-04", Content: "two", Tier: "archive"},
	}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo-a", "", docs); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE memory_entries SET tier='core' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	docs = docs[:1]
	docs[0].Title = "One updated"
	// The no-op skip treats an identical source_hash as an unchanged file, and
	// the scan pipeline derives the hash from the same bytes as every content
	// field - so a rescan that changes the title always changes the hash too.
	// The update must carry one, or the skip correctly leaves the row alone.
	docs[0].SourceHash = "h1b"
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo-a", "", docs); err != nil {
		t.Fatal(err)
	}
	var count, deleted int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_entries WHERE project_id='repo-a'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_entries WHERE id='two'`).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if count != 1 || deleted != 0 {
		t.Fatalf("memory rows count=%d deleted-row=%d, want one current row", count, deleted)
	}
	var title, tier string
	if err := store.db.QueryRow(`SELECT title, tier FROM memory_entries WHERE id='one'`).Scan(&title, &tier); err != nil {
		t.Fatal(err)
	}
	if title != "One updated" || tier != "core" {
		t.Fatalf("updated row title=%q tier=%q, want updated title and preserved core tier", title, tier)
	}
}

func TestSyncMemoryIndexIsolatesProjects(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := MemoryIndexDocument{ID: "same", Scope: "project", SourcePath: "/memory.md", SourceHash: "h", Title: "A", Summary: "a", Verdict: "good", Created: "2026-09-04", Content: "a", Tier: "archive"}
	for _, project := range []string{"repo-a", "repo-b"} {
		doc.ProjectID = project
		if err := store.SyncMemoryIndex(context.Background(), "project", project, "", []MemoryIndexDocument{doc}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_entries WHERE id='same'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("same ID count=%d, want two project-scoped rows", count)
	}
}

func TestFindAndPromotePreferProjectOnSharedID(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project := MemoryIndexDocument{ID: "same", Scope: "project", ProjectID: "repo", SourcePath: "/project.md", SourceHash: "p", Title: "Project", Summary: "project", Verdict: "good", Created: "2026-09-04", Content: "project"}
	org := MemoryIndexDocument{ID: "same", Scope: "org", OrgID: "acme", SourcePath: "/org.md", SourceHash: "o", Title: "Org", Summary: "org", Verdict: "good", Created: "2026-09-04", Content: "org"}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", []MemoryIndexDocument{project}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncMemoryIndex(context.Background(), "org", "", "acme", []MemoryIndexDocument{org}); err != nil {
		t.Fatal(err)
	}
	found, err := store.FindMemoryIndexEntry(context.Background(), "same", "repo", "acme")
	if err != nil || found.Scope != "project" {
		t.Fatalf("found=%+v err=%v, want project entry", found, err)
	}
	if err := store.PromoteMemoryIndexEntry(context.Background(), "same", "repo", "acme"); err != nil {
		t.Fatal(err)
	}
	var projectTier, orgTier string
	if err := store.db.QueryRow(`SELECT tier FROM memory_entries WHERE scope='project'`).Scan(&projectTier); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT tier FROM memory_entries WHERE scope='org'`).Scan(&orgTier); err != nil {
		t.Fatal(err)
	}
	if projectTier != "core" || orgTier != "archive" {
		t.Fatalf("tiers project=%q org=%q, want core/archive", projectTier, orgTier)
	}
}

func TestSyncMemoryIndexRejectsMismatchedProject(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	err = store.SyncMemoryIndex(context.Background(), "project", "repo-a", "", []MemoryIndexDocument{{ProjectID: "repo-b", Scope: "project"}})
	if err == nil {
		t.Fatal("SyncMemoryIndex accepted a document for another project")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Fatalf("error = %v, want project_id validation", err)
	}
}

func TestSyncMemoryIndexCanClearEmptyOrganizationScan(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := MemoryIndexDocument{ID: "org-one", Scope: "org", OrgID: "acme", SourcePath: "/org/one.md", SourceHash: "h", Title: "One", Summary: "one", Verdict: "good", Created: "2026-09-04", Content: "one"}
	if err := store.SyncMemoryIndex(context.Background(), "org", "", "acme", []MemoryIndexDocument{doc}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncMemoryIndex(context.Background(), "org", "", "acme", nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_entries WHERE scope='org' AND org_id='acme'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("organization rows after empty scan = %d, want 0", count)
	}
}

func TestSyncMemoryIndexRejectsDuplicateIDs(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	docs := []MemoryIndexDocument{
		{ID: "same", Scope: "project", ProjectID: "repo", SourcePath: "/one.md", SourceHash: "1", Title: "One", Summary: "one", Verdict: "good", Content: "one"},
		{ID: "same", Scope: "project", ProjectID: "repo", SourcePath: "/two.md", SourceHash: "2", Title: "Two", Summary: "two", Verdict: "good", Content: "two"},
	}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", docs); err == nil {
		t.Fatal("SyncMemoryIndex accepted duplicate IDs")
	}
}

func TestSyncMemoryIndexRemovesOldIDForChangedSource(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old := MemoryIndexDocument{ID: "old", Scope: "project", ProjectID: "repo-a", SourcePath: "/repo/memory.md", SourceHash: "h1", Title: "Old", Summary: "old", Verdict: "good", Created: "2026-09-04", Content: "old"}
	updated := old
	updated.ID, updated.SourceHash, updated.Title, updated.Content = "new", "h2", "New", "new"
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo-a", "", []MemoryIndexDocument{old}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo-a", "", []MemoryIndexDocument{updated}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_entries WHERE project_id='repo-a'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rows after source update = %d, want 1", count)
	}
}

func TestSyncMemoryIndexRejectsInvalidTopLevelScope(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SyncMemoryIndex(context.Background(), "bogus", "repo", "", nil); err == nil {
		t.Fatal("SyncMemoryIndex accepted an invalid top-level scope")
	}
}

func TestSyncMemoryIndexPropagatesInvalidOrgIDNormalization(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SyncMemoryIndex(context.Background(), "org", "", "/leading-slash", nil); err == nil {
		t.Fatal("SyncMemoryIndex accepted an org_id normalizeMemoryOrgID rejects")
	}
}

func TestNormalizeMemoryOrgIDRejectsFormatAndCharacterViolations(t *testing.T) {
	for _, bad := range []string{"", "/leading", "trailing/", "has..dots"} {
		if _, err := normalizeMemoryOrgID(bad); err == nil {
			t.Errorf("normalizeMemoryOrgID(%q) accepted an invalid format", bad)
		}
	}
	if _, err := normalizeMemoryOrgID("has space"); err == nil {
		t.Fatal("normalizeMemoryOrgID accepted an unsupported character")
	}
}

func TestSyncMemoryIndexPropagatesLoadScopeStateErrors(t *testing.T) {
	t.Run("memory_sources query fails", func(t *testing.T) {
		store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.db.Exec(`DROP TABLE memory_sources`); err != nil {
			t.Fatal(err)
		}
		if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", nil); err == nil {
			t.Fatal("SyncMemoryIndex did not propagate a broken memory_sources table")
		}
	})
	t.Run("memory_entries query fails", func(t *testing.T) {
		store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.db.Exec(`DROP TABLE memory_entries`); err != nil {
			t.Fatal(err)
		}
		if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", nil); err == nil {
			t.Fatal("SyncMemoryIndex did not propagate a broken memory_entries table")
		}
	})
}

func TestSyncMemoryIndexRejectsInvalidDocScope(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	docs := []MemoryIndexDocument{{ID: "x", Scope: "bogus", ProjectID: "repo", SourcePath: "/x.md", SourceHash: "h", Verdict: "good", Content: "x"}}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", docs); err == nil {
		t.Fatal("SyncMemoryIndex accepted a document with an unrecognized scope")
	}
}

func TestSyncMemoryIndexRejectsDocScopeNotMatchingScan(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	docs := []MemoryIndexDocument{{ID: "x", Scope: "org", OrgID: "acme", SourcePath: "/x.md", SourceHash: "h", Verdict: "good", Content: "x"}}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", docs); err == nil {
		t.Fatal("SyncMemoryIndex accepted an org-scoped document in a project scan")
	}
}

func TestSyncMemoryIndexRejectsOrgDocMismatch(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	docs := []MemoryIndexDocument{{ID: "x", Scope: "org", OrgID: "other-org", SourcePath: "/x.md", SourceHash: "h", Verdict: "good", Content: "x"}}
	if err := store.SyncMemoryIndex(context.Background(), "org", "", "acme", docs); err == nil {
		t.Fatal("SyncMemoryIndex accepted a document whose org_id does not match the scan")
	}
}

func TestSyncMemoryIndexRejectsMissingIDOrSourcePath(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, doc := range []MemoryIndexDocument{
		{ID: "", Scope: "project", ProjectID: "repo", SourcePath: "/x.md", SourceHash: "h", Verdict: "good", Content: "x"},
		{ID: "x", Scope: "project", ProjectID: "repo", SourcePath: "", SourceHash: "h", Verdict: "good", Content: "x"},
	} {
		if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", []MemoryIndexDocument{doc}); err == nil {
			t.Errorf("SyncMemoryIndex accepted a document with an empty id or source_path: %+v", doc)
		}
	}
}

func TestSyncMemoryIndexRejectsDuplicateSourcePath(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	docs := []MemoryIndexDocument{
		{ID: "one", Scope: "project", ProjectID: "repo", SourcePath: "/same.md", SourceHash: "h1", Verdict: "good", Content: "one"},
		{ID: "two", Scope: "project", ProjectID: "repo", SourcePath: "/same.md", SourceHash: "h2", Verdict: "good", Content: "two"},
	}
	err = store.SyncMemoryIndex(context.Background(), "project", "repo", "", docs)
	if err == nil {
		t.Fatal("SyncMemoryIndex accepted two documents (different IDs) at the same source_path")
	}
	if !strings.Contains(err.Error(), "duplicate memory index source") {
		t.Fatalf("error = %v, want duplicate source validation", err)
	}
}

func TestSyncMemoryIndexRejectsMissingScopeIdentity(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SyncMemoryIndex(context.Background(), "project", "", "", nil); err == nil {
		t.Fatal("SyncMemoryIndex accepted a project scan with no project_id")
	}
	if err := store.SyncMemoryIndex(context.Background(), "org", "", "", nil); err == nil {
		t.Fatal("SyncMemoryIndex accepted an organization scan with no org_id")
	}
}

// TestSyncMemoryIndexPropagatesUpsertCheckConstraintViolation forces
// upsertMemoryIndexDocument's UPDATE to violate memory_entries' own
// `CHECK(verdict IN ('good','bad','mixed','neutral'))` constraint
// (context_schema_v16.go): the first sync creates the row normally, and the
// second sync rewrites it with an out-of-range Verdict and a changed
// SourceHash (so memoryIndexDocUnchanged's skip does not short-circuit the
// upsert). The resulting SQL error must surface through both
// upsertMemoryIndexDocument and syncMemoryIndexOnce's own propagation.
func TestSyncMemoryIndexPropagatesUpsertCheckConstraintViolation(t *testing.T) {
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	doc := MemoryIndexDocument{ID: "x", Scope: "project", ProjectID: "repo", SourcePath: "/x.md", SourceHash: "h1", Verdict: "good", Content: "x"}
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", []MemoryIndexDocument{doc}); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	doc.SourceHash, doc.Verdict = "h2", "bogus-verdict"
	if err := store.SyncMemoryIndex(context.Background(), "project", "repo", "", []MemoryIndexDocument{doc}); err == nil {
		t.Fatal("SyncMemoryIndex accepted a verdict the memory_entries CHECK constraint rejects")
	}
}
