package memory

// Regression tests for defects found by the post-landing hostile audit:
//   - single-line fields (title/tags/references) must reject line breaks so
//     Render/Parse round-trip and the stored template stays strict;
//   - identical re-saves must be idempotent at capacity on BOTH backends;
//   - both backends must match tags and references in search;
//   - the org store is created only when org_id is configured, and is private;
//   - every sqlite save checkpoints WAL so the main file is always current.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestEntryValidateRejectsLineBreaksInSingleLineFields(t *testing.T) {
	base := testEntry("ok", ScopeProject)
	cases := map[string]func(*Entry){
		"title newline": func(e *Entry) { e.Title = "line1\nline2" },
		"title tab":     func(e *Entry) { e.Title = "line1\tline2" },
		"tag newline":   func(e *Entry) { e.Tags = []string{"a\nb"} },
		"tag tab":       func(e *Entry) { e.Tags = []string{"a\tb"} },
		"ref newline":   func(e *Entry) { e.References = []string{"a\nb"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := base
			mutate(&e)
			if err := e.Validate(Limits{}); err == nil {
				t.Fatalf("Validate accepted a line break in a single-line field")
			}
		})
	}
	// Multi-line bodies stay valid: they are stored as rendered sections.
	multi := base
	multi.Summary = "first\nsecond"
	multi.Good = "- a\n- b"
	multi.Why = "because\nreasons"
	if err := multi.Validate(Limits{}); err != nil {
		t.Fatalf("multi-line body must stay valid: %v", err)
	}
}

func TestEntryParseRenderRoundTripBodyNewlines(t *testing.T) {
	e := testEntry("multi-line body", ScopeProject)
	e.Summary = "first\nsecond"
	e.Good = "- a\n- b"
	e.Bad = "- c"
	e.Why = "because\nreasons"
	if err := e.Validate(Limits{}); err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse([]byte(e.Render()))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Title != e.Title || parsed.Summary != e.Summary || parsed.Good != e.Good ||
		parsed.Bad != e.Bad || parsed.Why != e.Why {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", parsed, e)
	}
}

func TestStoreSaveIdempotentAtCapacity(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "") // MaxEntries = 5
			ctx := context.Background()
			var first Result
			for i := 0; i < 5; i++ {
				r, err := s.Save(ctx, testEntry(fmt.Sprintf("entry-%d", i), ScopeProject))
				if err != nil {
					t.Fatalf("save %d: %v", i, err)
				}
				if i == 0 {
					first = r
				}
			}
			again, err := s.Save(ctx, testEntry("entry-0", ScopeProject))
			if err != nil {
				t.Fatalf("identical re-save at capacity must be idempotent, got: %v", err)
			}
			if again.ID != first.ID {
				t.Errorf("re-save id = %q, want %q", again.ID, first.ID)
			}
			n, err := s.Count(ctx, ScopeProject)
			if err != nil {
				t.Fatal(err)
			}
			if n != 5 {
				t.Errorf("count after re-save = %d, want 5", n)
			}
		})
	}
}

func TestStoreSearchMatchesTagsAndReferences(t *testing.T) {
	for _, backend := range []string{"sqlite", "memory"} {
		t.Run(backend, func(t *testing.T) {
			s := newTestStore(t, backend, "")
			ctx := context.Background()
			e := testEntry("tagged entry", ScopeProject)
			e.Tags = []string{"concurrency"}
			e.References = []string{"https://example.com/notes"}
			if _, err := s.Save(ctx, e); err != nil {
				t.Fatal(err)
			}
			tag, err := s.Search(ctx, Query{Text: "concurrency", Scope: ScopeProject})
			if err != nil {
				t.Fatal(err)
			}
			if len(tag) != 1 {
				t.Errorf("%s: tag search = %d results, want 1 (both backends must match tags)", backend, len(tag))
			}
			ref, err := s.Search(ctx, Query{Text: "example.com", Scope: ScopeProject})
			if err != nil {
				t.Fatal(err)
			}
			if len(ref) != 1 {
				t.Errorf("%s: reference search = %d results, want 1 (both backends must match references)", backend, len(ref))
			}
		})
	}
}

func TestSQLiteOrgStoreNotCreatedWithoutOrgID(t *testing.T) {
	dir := t.TempDir()
	orgPath := filepath.Join(dir, "org.db")
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      filepath.Join(dir, "project.db"),
		OrgPath:          orgPath,
		OrgID:            "",
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(orgPath); !os.IsNotExist(err) {
		t.Errorf("org store %s must not be created when org_id is unset", orgPath)
	}
}

func TestSQLiteOrgStorePermissions(t *testing.T) {
	dir := t.TempDir()
	orgPath := filepath.Join(dir, "org.db")
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      filepath.Join(dir, "project.db"),
		OrgPath:          orgPath,
		OrgID:            "github.com/acme",
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := os.Stat(orgPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("org store mode = %o, want 600 (user-owned private store)", perm)
	}
	dirSt, err := os.Stat(filepath.Dir(orgPath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirSt.Mode().Perm(); perm != 0o700 {
		t.Errorf("org store dir mode = %o, want 700", perm)
	}
}

func TestSQLiteSaveCheckpointsWAL(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project.db")
	wal := project + "-wal"
	cfg := Config{
		Backend:          BackendSQLite,
		ProjectPath:      project,
		MaxEntryBytes:    8192,
		MaxEntries:       5,
		MaxSearchResults: 8,
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Save(ctx, testEntry("checkpointed", ScopeProject)); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(wal); err != nil {
		t.Fatalf("stat wal: %v", err)
	} else if st.Size() != 0 {
		t.Errorf("wal size after save = %d, want 0 (save must checkpoint into the main file)", st.Size())
	}
	// The main file alone must already contain the row: a fresh connection
	// (no WAL recovery needed) reads it back.
	db, err := sql.Open("sqlite", project)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&n); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if n != 1 {
		t.Errorf("main file row count = %d, want 1 (committed DB must not lose recent writes)", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// SQLite removes the WAL file entirely on a clean close after a TRUNCATE
	// checkpoint; either outcome proves no frames were left stranded.
	if st, err := os.Stat(wal); err == nil && st.Size() != 0 {
		t.Errorf("wal size after close = %d, want 0 (or the file removed)", st.Size())
	}
}
