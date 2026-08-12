package memory

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoreReadOnlySearchDoesNotModifyFile is the byte-stability regression
// for the read-only memory store: a search-only session must never touch the
// committed database file. Before the fix the sqlite open path ran
// journal_mode=WAL, the schema CREATE, the FTS5 rebuild backfill, and Close's
// wal_checkpoint(TRUNCATE) even for a search-only session, so
// Open+Search+Close changed the committed file's bytes. The read-only store
// must return identical results to a read-write search while leaving the file
// byte-for-byte untouched.
func TestStoreReadOnlySearchDoesNotModifyFile(t *testing.T) {
	dir := t.TempDir()
	rwPath := filepath.Join(dir, "project.db")
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      rwPath,
		MaxEntryBytes:    8192,
		MaxEntries:       50,
		MaxSearchResults: 8,
	}
	rw, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open rw: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := rw.Save(ctx, testEntry(fmt.Sprintf("ro-entry-%d", i), ScopeProject)); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	want, err := rw.Search(ctx, Query{Text: "ro-entry", Scope: ScopeProject})
	if err != nil {
		t.Fatalf("rw search: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("Close rw: %v", err)
	}

	// Copy the committed file: the read-only session must return the same
	// results as the read-write search while leaving the copy untouched.
	committed, err := os.ReadFile(rwPath)
	if err != nil {
		t.Fatal(err)
	}
	roPath := filepath.Join(dir, "ro.db")
	if err := os.WriteFile(roPath, committed, 0o644); err != nil {
		t.Fatal(err)
	}
	roCfg := cfg
	roCfg.ProjectPath = roPath
	roCfg.ReadOnly = true
	ro, err := Open(roCfg)
	if err != nil {
		t.Fatalf("Open read-only: %v", err)
	}
	got, err := ro.Search(ctx, Query{Text: "ro-entry", Scope: ScopeProject})
	if err != nil {
		t.Fatalf("read-only search: %v", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("Close read-only: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("read-only search = %d results, rw search = %d; want equal", len(got), len(want))
	}
	for i := range got {
		if got[i].Title != want[i].Title || got[i].Snippet != want[i].Snippet {
			t.Fatalf("result %d differs: %+v vs %+v", i, got[i], want[i])
		}
	}
	after, err := os.ReadFile(roPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, after) {
		t.Fatalf("read-only open+search+close changed the database file bytes (len %d -> %d)", len(committed), len(after))
	}
}

// TestStoreReadOnlyRefusesWrites pins that a read-only store refuses Save on
// every backend with a clear error.
func TestStoreReadOnlyRefusesWrites(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			cfg := Config{
				Backend:          backend,
				ProjectPath:      filepath.Join(t.TempDir(), "project.db"),
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
			_, err = s.Save(context.Background(), testEntry("ro", ScopeProject))
			if err == nil || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("Save on a read-only store must fail with a read-only error, got %v", err)
			}
		})
	}
}

// TestSQLiteMemoryDSNReadOnly pins the read-only DSN contract: it carries
// query_only(1), keeps busy_timeout, never sets journal_mode(WAL), and
// preserves the same '?'-escaping contract as sqliteMemoryDSN (a '?' path
// becomes a file: URI whose path portion percent-decodes back to the input).
func TestSQLiteMemoryDSNReadOnly(t *testing.T) {
	t.Run("plain path keeps the read-only params shape", func(t *testing.T) {
		dsn := sqliteMemoryDSNReadOnly("events.db")
		if !strings.Contains(dsn, "query_only(1)") {
			t.Errorf("read-only DSN = %q, want query_only(1)", dsn)
		}
		if strings.Contains(dsn, "journal_mode") {
			t.Errorf("read-only DSN = %q, must not set journal_mode(WAL)", dsn)
		}
		if !strings.Contains(dsn, "busy_timeout(5000)") {
			t.Errorf("read-only DSN = %q, must keep busy_timeout", dsn)
		}
		if want := "events.db?" + pragmaMemoryDSNReadOnlyParams; dsn != want {
			t.Errorf("read-only DSN = %q, want %q", dsn, want)
		}
	})
	t.Run("question mark path preserves the escaping contract", func(t *testing.T) {
		path := "ctx?name.db"
		dsn := sqliteMemoryDSNReadOnly(path)
		rest := strings.TrimPrefix(dsn, "file:")
		sep := strings.IndexByte(rest, '?')
		if sep < 0 {
			t.Fatalf("read-only DSN %q has no query separator", dsn)
		}
		encoded := rest[:sep]
		if strings.Contains(encoded, "?") {
			t.Fatalf("path portion %q still contains a literal '?'", encoded)
		}
		decoded, err := url.PathUnescape(encoded)
		if err != nil || decoded != path {
			t.Fatalf("PathUnescape(%q) = %q, %v; want %q", encoded, decoded, err, path)
		}
		if got := rest[sep+1:]; got != pragmaMemoryDSNReadOnlyParams {
			t.Fatalf("read-only DSN query = %q, want %q", got, pragmaMemoryDSNReadOnlyParams)
		}
	})
}
