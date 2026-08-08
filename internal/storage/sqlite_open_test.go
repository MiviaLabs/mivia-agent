package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenSQLiteBareFilenameOpensDatabaseFile pins the DC-10 path-handling
// fix: a bare relative path (no directory separator) must open as a database
// file in the current directory, never as a directory. Before the fix
// OpenSQLite computed the parent directory as the filename itself, so
// os.MkdirAll created a DIRECTORY named like the DB file and the WAL PRAGMA
// then failed on the directory path - leaving a stray directory behind and
// the configured store unopenable. Caller path: user config
// [subagents] store_path = "ledger.db" (config.ExpandPath leaves a bare
// relative path unchanged) reaches storage.OpenSQLite via
// internal/cli/context_setup.go openContextStorePath and
// internal/cli/orchestration_state.go openDurableLedgerRepo/openSharedSQLite.
func TestOpenSQLiteBareFilenameOpensDatabaseFile(t *testing.T) {
	t.Chdir(t.TempDir())

	s, err := OpenSQLite("bare.db")
	if err != nil {
		t.Fatalf("OpenSQLite(%q): %v", "bare.db", err)
	}
	defer s.Close()

	st, err := os.Stat("bare.db")
	if err != nil {
		t.Fatalf("stat %q: %v", "bare.db", err)
	}
	if !st.Mode().IsRegular() {
		t.Fatalf("%q mode = %v, want a regular file and not a directory", "bare.db", st.Mode())
	}

	ctx := context.Background()
	ev := Event{ID: "bare-1", RunID: "bare-run", Sequence: 1, Kind: "test", Payload: []byte("p")}
	if err := s.Append(ctx, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Events(ctx, "bare-run")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 1 || got[0].ID != "bare-1" || got[0].Sequence != 1 || string(got[0].Payload) != "p" {
		t.Fatalf("Events = %+v, want the appended event", got)
	}
}

// TestOpenSQLiteRejectsEmptyPath preserves the degenerate-input contract:
// OpenSQLite("") must return an error. Today it errors only after
// os.MkdirAll("") fails; the fix rejects the empty path explicitly before any
// filesystem work. Green both before and after the change.
func TestOpenSQLiteRejectsEmptyPath(t *testing.T) {
	t.Chdir(t.TempDir())
	if s, err := OpenSQLite(""); err == nil {
		s.Close()
		t.Fatal("OpenSQLite(\"\") succeeded, want an error")
	}
}

// TestOpenSQLitePathShapeBoundary pins the path shapes the fix must not
// disturb. Each row runs in its own fresh cwd so no row pollutes another.
//   - bare: succeeds, DB is a regular file (parity with the main regression).
//   - nested relative: succeeds, parent "sub" is created as a directory.
//   - absolute: succeeds unchanged.
//   - whitespace only: errors - the fix's explicit rejection (today it errors
//     only accidentally after MkdirAll creates a directory named "   ").
//   - trailing separator "dir/": errors at DB open because the DSN resolves
//     to a directory - filepath.Dir("dir/") == "dir" matches today's
//     LastIndexAny slice, so this degenerate parity is unchanged.
func TestOpenSQLitePathShapeBoundary(t *testing.T) {
	t.Run("bare", func(t *testing.T) {
		t.Chdir(t.TempDir())
		s, err := OpenSQLite("events.db")
		if err != nil {
			t.Fatalf("OpenSQLite(%q): %v", "events.db", err)
		}
		defer s.Close()
		st, err := os.Stat("events.db")
		if err != nil {
			t.Fatalf("stat %q: %v", "events.db", err)
		}
		if !st.Mode().IsRegular() {
			t.Fatalf("%q mode = %v, want a regular file", "events.db", st.Mode())
		}
	})
	t.Run("nested relative", func(t *testing.T) {
		t.Chdir(t.TempDir())
		s, err := OpenSQLite(filepath.Join("sub", "events.db"))
		if err != nil {
			t.Fatalf("OpenSQLite(nested relative): %v", err)
		}
		defer s.Close()
		st, err := os.Stat(filepath.Join("sub", "events.db"))
		if err != nil {
			t.Fatalf("stat nested: %v", err)
		}
		if !st.Mode().IsRegular() {
			t.Fatalf("nested mode = %v, want a regular file", st.Mode())
		}
	})
	t.Run("absolute", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "abs.db")
		s, err := OpenSQLite(path)
		if err != nil {
			t.Fatalf("OpenSQLite(absolute): %v", err)
		}
		defer s.Close()
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat absolute: %v", err)
		}
		if !st.Mode().IsRegular() {
			t.Fatalf("absolute mode = %v, want a regular file", st.Mode())
		}
	})
	t.Run("whitespace only", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if s, err := OpenSQLite("   "); err == nil {
			s.Close()
			t.Fatal("OpenSQLite(\"   \") succeeded, want an error")
		}
	})
	t.Run("trailing separator", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if s, err := OpenSQLite("dir" + string(os.PathSeparator)); err == nil {
			s.Close()
			t.Fatal("OpenSQLite(\"dir/\") succeeded, want an error at DB open")
		}
	})
}
