package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// insertMemoryTierEntry writes one memory_entries row directly, matching the
// raw store.db.Exec pattern already used elsewhere in this package's tests,
// so PromoteMemoryIndexEntry/DeleteMemoryIndexEntry/CoreMemoryIndexEntries
// can be exercised without going through the SyncMemoryIndex pipeline.
func insertMemoryTierEntry(t *testing.T, store *SQLite, id, scope, projectID, orgID, tier string) {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO memory_entries (id, scope, project_id, org_id, source_path, source_hash, title, summary, verdict, tags, created, content, tier) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, scope, projectID, orgID, "/"+id+".md", "hash-"+id, "title-"+id, "summary", "neutral", "", "2026-09-04", "content", tier)
	if err != nil {
		t.Fatalf("insert memory entry %s: %v", id, err)
	}
}

func openMemoryTierStore(t *testing.T) *SQLite {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestPromoteMemoryIndexEntryPropagatesBeginWriteError(t *testing.T) {
	store := openMemoryTierStore(t)
	insertMemoryTierEntry(t, store, "one", "project", "repo", "", "archive")
	if err := store.writeDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteMemoryIndexEntry(context.Background(), "one", "repo", ""); err == nil {
		t.Fatal("PromoteMemoryIndexEntry did not propagate a closed write pool")
	}
}

func TestPromoteMemoryIndexEntryPropagatesLookupQueryError(t *testing.T) {
	store := openMemoryTierStore(t)
	if _, err := store.db.Exec(`DROP TABLE memory_entries`); err != nil {
		t.Fatal(err)
	}
	err := store.PromoteMemoryIndexEntry(context.Background(), "one", "repo", "")
	if err == nil {
		t.Fatal("PromoteMemoryIndexEntry did not propagate a broken memory_entries table")
	}
	if err == ErrMemoryIndexEntryNotFound {
		t.Fatalf("expected a generic query error, got the not-found sentinel: %v", err)
	}
}

func TestPromoteMemoryIndexEntryRejectsWhenCoreTierIsFull(t *testing.T) {
	store := openMemoryTierStore(t)
	orgID := "acme"
	for i := 0; i < memoryIndexCoreTierCap; i++ {
		insertMemoryTierEntry(t, store, coreEntryID(i), "org", "", orgID, "core")
	}
	insertMemoryTierEntry(t, store, "candidate", "org", "", orgID, "archive")

	err := store.PromoteMemoryIndexEntry(context.Background(), "candidate", "", orgID)
	if err == nil {
		t.Fatal("PromoteMemoryIndexEntry accepted a promotion into a full core tier")
	}
	var tier string
	if scanErr := store.db.QueryRow(`SELECT tier FROM memory_entries WHERE id='candidate'`).Scan(&tier); scanErr != nil {
		t.Fatal(scanErr)
	}
	if tier != "archive" {
		t.Fatalf("candidate tier = %q, want archive (unchanged) after a rejected promotion", tier)
	}
}

func coreEntryID(i int) string {
	return "core-" + string(rune('a'+i))
}

func TestPromoteMemoryIndexEntryPropagatesUpdateExecError(t *testing.T) {
	store := openMemoryTierStore(t)
	insertMemoryTierEntry(t, store, "one", "project", "repo", "", "archive")
	// A BEFORE UPDATE trigger that aborts is a real SQL failure the same way
	// context_schema_v16.go's CHECK constraints are, without corrupting the
	// schema: it forces tx.ExecContext's UPDATE to fail after the earlier
	// SELECT/COUNT queries in the same call have already succeeded.
	if _, err := store.db.Exec(`CREATE TRIGGER block_promote_update BEFORE UPDATE OF tier ON memory_entries WHEN NEW.tier='core' BEGIN SELECT RAISE(ABORT, 'blocked by test trigger'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteMemoryIndexEntry(context.Background(), "one", "repo", ""); err == nil {
		t.Fatal("PromoteMemoryIndexEntry did not propagate an UPDATE failure")
	}
}

func TestCoreMemoryIndexEntriesRejectsInvalidScope(t *testing.T) {
	store := openMemoryTierStore(t)
	if _, err := store.CoreMemoryIndexEntries(context.Background(), "bogus", "repo", ""); err == nil {
		t.Fatal("CoreMemoryIndexEntries accepted an invalid scope")
	}
}

func TestCoreMemoryIndexEntriesPropagatesQueryError(t *testing.T) {
	store := openMemoryTierStore(t)
	if _, err := store.db.Exec(`DROP TABLE memory_entries`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CoreMemoryIndexEntries(context.Background(), "project", "repo", ""); err == nil {
		t.Fatal("CoreMemoryIndexEntries did not propagate a broken memory_entries table")
	}
}

func TestCoreMemoryIndexEntriesReturnsBoundedCoreTierForOrgScope(t *testing.T) {
	store := openMemoryTierStore(t)
	insertMemoryTierEntry(t, store, "core-one", "org", "", "acme", "core")
	insertMemoryTierEntry(t, store, "archived-one", "org", "", "acme", "archive")
	docs, err := store.CoreMemoryIndexEntries(context.Background(), "org", "", "acme")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].ID != "core-one" {
		t.Fatalf("CoreMemoryIndexEntries = %+v, want only the core-tier org entry", docs)
	}
}

func TestDeleteMemoryIndexEntryRemovesEntry(t *testing.T) {
	store := openMemoryTierStore(t)
	insertMemoryTierEntry(t, store, "one", "project", "repo", "", "archive")
	if err := store.DeleteMemoryIndexEntry(context.Background(), "one", "repo", ""); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_entries WHERE id='one'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("memory_entries rows for id=one = %d, want 0 after delete", count)
	}
}

func TestDeleteMemoryIndexEntryReturnsNotFoundWhenMissing(t *testing.T) {
	store := openMemoryTierStore(t)
	err := store.DeleteMemoryIndexEntry(context.Background(), "missing", "repo", "")
	if err != ErrMemoryIndexEntryNotFound {
		t.Fatalf("DeleteMemoryIndexEntry error = %v, want ErrMemoryIndexEntryNotFound", err)
	}
}

func TestDeleteMemoryIndexEntryPropagatesBeginWriteError(t *testing.T) {
	store := openMemoryTierStore(t)
	insertMemoryTierEntry(t, store, "one", "project", "repo", "", "archive")
	if err := store.writeDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMemoryIndexEntry(context.Background(), "one", "repo", ""); err == nil {
		t.Fatal("DeleteMemoryIndexEntry did not propagate a closed write pool")
	}
}

func TestDeleteMemoryIndexEntryPropagatesExecError(t *testing.T) {
	store := openMemoryTierStore(t)
	insertMemoryTierEntry(t, store, "one", "project", "repo", "", "archive")
	// Same technique as the promote-UPDATE trigger above: a real SQL failure
	// (a BEFORE DELETE trigger that aborts) forces tx.ExecContext's DELETE to
	// fail without touching the schema's own integrity.
	if _, err := store.db.Exec(`CREATE TRIGGER block_delete BEFORE DELETE ON memory_entries BEGIN SELECT RAISE(ABORT, 'blocked by test trigger'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteMemoryIndexEntry(context.Background(), "one", "repo", ""); err == nil {
		t.Fatal("DeleteMemoryIndexEntry did not propagate a DELETE failure")
	}
}
