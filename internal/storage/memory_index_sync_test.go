package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// memoryIndexSyncDocs builds the document set the sync tests scan: two docs
// for project scope repo-a whose SourcePath values point inside dir. T2
// creates the real .md files at those paths; T1 and T3 only need the
// identities, because storage never re-reads the files.
func memoryIndexSyncDocs(dir string) []MemoryIndexDocument {
	return []MemoryIndexDocument{
		{ID: "one", Scope: "project", ProjectID: "repo-a", SourcePath: filepath.Join(dir, "one.md"), SourceHash: "h1", Title: "One", Summary: "first", Verdict: "good", Created: "2026-09-04", Content: "one"},
		{ID: "two", Scope: "project", ProjectID: "repo-a", SourcePath: filepath.Join(dir, "two.md"), SourceHash: "h2", Title: "Two", Summary: "second", Verdict: "neutral", Created: "2026-09-04", Content: "two"},
	}
}

// TestSyncMemoryIndexRetriesWhenSiblingHoldsWriteLock proves SyncMemoryIndex
// survives a competing writer that holds the database write lock longer than
// busy_timeout (5000 ms at open, sqlite.go). The holder pins a raw separate
// connection with BEGIN IMMEDIATE; before the retry wrapper existed the first
// attempt blocked for the full busy_timeout and then failed SQLITE_BUSY with
// zero retries.
//
// Duration: ~7 s by design, deterministic. The holder releases at 6 s;
// attempt 1 spends 5 s inside busy_timeout and fails, and the first retry
// (~100 ms later) lands once the holder is gone. The retrySQLiteBusy budget
// (~18 s) bounds the wait with room to spare.
func TestSyncMemoryIndexRetriesWhenSiblingHoldsWriteLock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "context.db")
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	holder, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	conn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("holder BEGIN IMMEDIATE: %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		})
	}
	go func() {
		time.Sleep(6 * time.Second)
		release()
	}()

	dir := t.TempDir()
	errCh := make(chan error, 1)
	go func() {
		errCh <- store.SyncMemoryIndex(ctx, "project", "repo-a", "", memoryIndexSyncDocs(dir))
	}()
	select {
	case err := <-errCh:
		release()
		if err != nil {
			t.Fatalf("SyncMemoryIndex: %v, want success via busy retry", err)
		}
	case <-time.After(45 * time.Second):
		release()
		t.Fatal("SyncMemoryIndex did not return within 45s")
	}
	assertMemoryIndexSyncRows(t, store)
}

// assertMemoryIndexSyncRows checks both sync tables landed exactly the two
// repo-a rows the shared doc set produces.
func assertMemoryIndexSyncRows(t *testing.T, store *SQLite) {
	t.Helper()
	var entries, sources int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_entries WHERE project_id='repo-a'`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM memory_sources WHERE project_id='repo-a'`).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if entries != 2 || sources != 2 {
		t.Fatalf("rows after sync: entries=%d sources=%d, want 2 and 2", entries, sources)
	}
}

// memoryIndexTimestamps snapshots identity and indexed_at of every repo-a row
// in both sync tables, keyed so a before/after comparison names the row that
// changed.
func memoryIndexTimestamps(t *testing.T, store *SQLite) map[string]string {
	t.Helper()
	rows, err := store.db.Query(`SELECT 'entry:'||id, indexed_at FROM memory_entries WHERE project_id='repo-a'
		UNION ALL SELECT 'source:'||source_path, indexed_at FROM memory_sources WHERE project_id='repo-a'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var key, ts string
		if err := rows.Scan(&key, &ts); err != nil {
			t.Fatal(err)
		}
		out[key] = ts
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSyncMemoryIndexSecondUnchangedSyncWritesNoRows syncs a directory of two
// Markdown memory files twice. CURRENT_TIMESTAMP has one-second granularity,
// so the test waits ~1.1 s between the syncs: without that gap a writing
// (non-skipping) sync could land in the same second and the indexed_at
// comparison would pass vacuously. The second sync must change no row at all
// - identical indexed_at in both tables - and an operator promotion done
// between the syncs must survive untouched.
func TestSyncMemoryIndexSecondUnchangedSyncWritesNoRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	docs := memoryIndexSyncDocs(dir)
	for _, doc := range docs {
		data := fmt.Sprintf("---\nid: %s\ntitle: %s\ncontent: memory\n---\n\nBody text.\n", doc.ID, doc.Title)
		if err := os.WriteFile(doc.SourcePath, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.SyncMemoryIndex(ctx, "project", "repo-a", "", docs); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteMemoryIndexEntry(ctx, "one", "repo-a", ""); err != nil {
		t.Fatalf("promote: %v", err)
	}
	before := memoryIndexTimestamps(t, store)
	time.Sleep(1100 * time.Millisecond)
	if err := store.SyncMemoryIndex(ctx, "project", "repo-a", "", docs); err != nil {
		t.Fatal(err)
	}
	after := memoryIndexTimestamps(t, store)
	if len(before) != len(after) {
		t.Fatalf("row count changed across the second sync: before=%d after=%d", len(before), len(after))
	}
	for key, ts := range before {
		if after[key] != ts {
			t.Fatalf("second sync changed row %s: indexed_at %q -> %q, want a no-op sync", key, ts, after[key])
		}
	}
	var tier string
	if err := store.db.QueryRow(`SELECT tier FROM memory_entries WHERE id='one'`).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != "core" {
		t.Fatalf("tier after second sync = %q, want the operator promotion preserved", tier)
	}
}

// TestSyncMemoryIndexConcurrentHandlesOnOneDatabase runs two stores on one
// database file and syncs both from two goroutines (~20 calls each) over
// slightly different document sets for the same scope. Cross-handle write
// contention must be absorbed by the busy retry, with no error escaping;
// afterwards one final sync must leave exactly the state a from-scratch scan
// of the same documents produces. Run with -race (package tests do).
func TestSyncMemoryIndexConcurrentHandlesOnOneDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "context.db")
	storeA, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()

	shared := MemoryIndexDocument{ID: "shared", Scope: "project", ProjectID: "repo-a", SourcePath: filepath.Join(dir, "shared.md"), SourceHash: "hs", Title: "Shared", Summary: "shared", Verdict: "neutral", Created: "2026-09-04", Content: "shared"}
	aOnly := MemoryIndexDocument{ID: "a-only", Scope: "project", ProjectID: "repo-a", SourcePath: filepath.Join(dir, "a-only.md"), SourceHash: "ha", Title: "A only", Summary: "a", Verdict: "good", Created: "2026-09-04", Content: "a"}
	bOnly := MemoryIndexDocument{ID: "b-only", Scope: "project", ProjectID: "repo-a", SourcePath: filepath.Join(dir, "b-only.md"), SourceHash: "hb", Title: "B only", Summary: "b", Verdict: "good", Created: "2026-09-04", Content: "b"}
	setA := []MemoryIndexDocument{shared, aOnly}
	setB := []MemoryIndexDocument{shared, bOnly}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i, store := range []*SQLite{storeA, storeB} {
		go func(i int, store *SQLite) {
			defer wg.Done()
			docs := setA
			if i == 1 {
				docs = setB
			}
			for n := 0; n < 20; n++ {
				if err := store.SyncMemoryIndex(ctx, "project", "repo-a", "", docs); err != nil {
					errs[i] = fmt.Errorf("handle %d sync %d: %w", i, n, err)
					return
				}
			}
		}(i, store)
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("concurrent syncs: %v, want every busy collision absorbed", err)
	}

	// One final sync must converge to what a from-scratch scan of setA
	// produces on a fresh database.
	if err := storeA.SyncMemoryIndex(ctx, "project", "repo-a", "", setA); err != nil {
		t.Fatal(err)
	}
	fromScratch, err := OpenSQLite(filepath.Join(dir, "scratch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fromScratch.Close()
	if err := fromScratch.SyncMemoryIndex(ctx, "project", "repo-a", "", setA); err != nil {
		t.Fatal(err)
	}
	assertSameMemoryIndexRows(t, storeA, fromScratch)
}

// assertSameMemoryIndexRows compares the repo-a identity rows (id, path,
// hash) of two stores. indexed_at is deliberately not compared: the stores
// synced at different moments.
func assertSameMemoryIndexRows(t *testing.T, got, want *SQLite) {
	t.Helper()
	gotEntries := memoryIndexEntryIdentities(t, got)
	wantEntries := memoryIndexEntryIdentities(t, want)
	if !reflect.DeepEqual(gotEntries, wantEntries) {
		t.Fatalf("entries after final sync = %v, want the from-scratch rows %v", gotEntries, wantEntries)
	}
	gotSources := memoryIndexSourceIdentities(t, got)
	wantSources := memoryIndexSourceIdentities(t, want)
	if !reflect.DeepEqual(gotSources, wantSources) {
		t.Fatalf("sources after final sync = %v, want the from-scratch rows %v", gotSources, wantSources)
	}
}

func memoryIndexEntryIdentities(t *testing.T, store *SQLite) []string {
	t.Helper()
	return memoryIndexStrings(t, store, `SELECT id||'|'||source_path||'|'||source_hash FROM memory_entries WHERE project_id='repo-a' ORDER BY id`)
}

func memoryIndexSourceIdentities(t *testing.T, store *SQLite) []string {
	t.Helper()
	return memoryIndexStrings(t, store, `SELECT source_path||'|'||source_hash FROM memory_sources WHERE project_id='repo-a' ORDER BY source_path`)
}

func memoryIndexStrings(t *testing.T, store *SQLite, query string) []string {
	t.Helper()
	rows, err := store.db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		out = append(out, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSyncMemoryIndexHealsDuplicateEntryRows covers the skip contract's
// multi-row clause: the schema does not enforce memory_entries source_path
// uniqueness (context_schema_v16), so a path carrying two rows is treated as
// changed and healed by the full upsert even when every hash matches.
func TestSyncMemoryIndexHealsDuplicateEntryRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	docs := memoryIndexSyncDocs(dir)
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "context.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SyncMemoryIndex(ctx, "project", "repo-a", "", docs); err != nil {
		t.Fatal(err)
	}
	path := docs[0].SourcePath
	ghost := `INSERT INTO memory_entries(id,scope,project_id,org_id,source_path,source_hash,title,summary,verdict,tags,created,content) VALUES('ghost','project','repo-a','','` + path + `','h1','Ghost','ghost','neutral','','2026-09-04','ghost')`
	if _, err := store.db.Exec(ghost); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncMemoryIndex(ctx, "project", "repo-a", "", docs); err != nil {
		t.Fatal(err)
	}
	identities := memoryIndexEntryIdentities(t, store)
	if len(identities) != 2 {
		t.Fatalf("rows after healing sync = %v, want the two scanned docs only", identities)
	}
	for _, identity := range identities {
		if strings.Contains(identity, "ghost") {
			t.Fatalf("duplicate-path row survived the sync: %v", identities)
		}
	}
}
