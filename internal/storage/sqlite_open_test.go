package storage

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestOpenSQLiteQuestionMarkPathOpensLiteralFile is the regression for the
// storage-dsn-question-mark-truncation bug: a relative store path containing a
// literal '?' must open as the literal file, never truncated at the '?'. Before
// the fix sqliteDSN returned "ledger?part.db?_pragma=..." and the
// modernc.org/sqlite v1.54.0 driver split the DSN at the first '?'
// (newConn's strings.IndexRune), so the store silently opened "ledger" - a
// different, wrong database file. '?' is not a legal Windows filename
// character, so the bug (and this regression) is POSIX-only.
func TestOpenSQLiteQuestionMarkPathOpensLiteralFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("'?' is not a legal Windows filename character; the bug is POSIX-only")
	}
	t.Chdir(t.TempDir())

	s, err := OpenSQLite("ledger?part.db")
	if err != nil {
		t.Fatalf("OpenSQLite(%q): %v", "ledger?part.db", err)
	}
	defer s.Close()

	st, err := os.Stat("ledger?part.db")
	if err != nil {
		t.Fatalf("stat %q: %v", "ledger?part.db", err)
	}
	if !st.Mode().IsRegular() {
		t.Fatalf("%q mode = %v, want a regular file", "ledger?part.db", st.Mode())
	}
	if _, err := os.Stat("ledger"); err == nil {
		t.Fatal("truncated file \"ledger\" exists: the store opened the wrong database file")
	}

	ctx := context.Background()
	ev := Event{ID: "q-1", RunID: "q-run", Sequence: 1, Kind: "test", Payload: []byte("p")}
	if err := s.Append(ctx, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Events(ctx, "q-run")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 1 || got[0].ID != "q-1" || got[0].Sequence != 1 || string(got[0].Payload) != "p" {
		t.Fatalf("Events = %+v, want the appended event", got)
	}
}

// TestOpenSQLiteQuestionMarkPathAbsolute is the absolute-path variant of the
// question-mark regression: filepath.Join(t.TempDir(), "ctx?name.db") must open
// as the literal file at that absolute path. Before the fix the DSN truncated
// at the first '?', so the driver created the sibling file ".../ctx" instead of
// ".../ctx?name.db".
func TestOpenSQLiteQuestionMarkPathAbsolute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("'?' is not a legal Windows filename character; the bug is POSIX-only")
	}
	path := filepath.Join(t.TempDir(), "ctx?name.db")

	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite(%q): %v", path, err)
	}
	defer s.Close()

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if !st.Mode().IsRegular() {
		t.Fatalf("%q mode = %v, want a regular file", path, st.Mode())
	}
	truncated := filepath.Join(filepath.Dir(path), "ctx")
	if _, err := os.Stat(truncated); err == nil {
		t.Fatalf("truncated file %q exists: the store opened the wrong database file", truncated)
	}

	ctx := context.Background()
	ev := Event{ID: "qabs-1", RunID: "qabs-run", Sequence: 1, Kind: "test", Payload: []byte("p")}
	if err := s.Append(ctx, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Events(ctx, "qabs-run")
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 1 || got[0].ID != "qabs-1" {
		t.Fatalf("Events = %+v, want the appended event", got)
	}
}

// TestOpenSQLiteQuestionMarkPathReopenPersists proves both opens address the
// same literal-'?' file: open at the '?'-path, append, Close, reopen the same
// path, and read the event back. Combined with the stat assertions this fails
// before the fix (a truncated sibling file exists and the literal path does
// not), and after the fix it pins the persistence parity of the file: URI form.
func TestOpenSQLiteQuestionMarkPathReopenPersists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("'?' is not a legal Windows filename character; the bug is POSIX-only")
	}
	t.Chdir(t.TempDir())

	path := "persist?db"
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite(%q): %v", path, err)
	}
	ctx := context.Background()
	ev := Event{ID: "persist-1", RunID: "persist-run", Sequence: 1, Kind: "test", Payload: []byte("p")}
	if err := s.Append(ctx, ev); err != nil {
		s.Close()
		t.Fatalf("Append: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("reopen OpenSQLite(%q): %v", path, err)
	}
	defer s2.Close()

	got, err := s2.Events(ctx, "persist-run")
	if err != nil {
		t.Fatalf("Events after reopen: %v", err)
	}
	if len(got) != 1 || got[0].ID != "persist-1" || string(got[0].Payload) != "p" {
		t.Fatalf("Events after reopen = %+v, want the appended event", got)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q after reopen: %v", path, err)
	}
	if !st.Mode().IsRegular() {
		t.Fatalf("%q mode = %v, want a regular file", path, st.Mode())
	}
	if _, err := os.Stat("persist"); err == nil {
		t.Fatal("truncated file \"persist\" exists: the store opened the wrong database file")
	}
}

// TestSQLiteDSNQuestionMarkUsesFileURI pins the DSN-shape contract directly,
// independent of the driver: plain paths keep the historical path+"?"+pragmas
// DSN byte-for-byte, and '?'-paths become a file: URI whose path portion
// contains no literal '?' and percent-decodes back to the input path. This
// fails before the fix because sqliteDSN("ledger?part.db") produced
// "ledger?part.db?_pragma=..." with no file: prefix and a literal '?' inside
// the filename portion.
func TestSQLiteDSNQuestionMarkUsesFileURI(t *testing.T) {
	t.Run("plain path keeps historical shape", func(t *testing.T) {
		for _, path := range []string{"events.db", "dir/events.db", "/tmp/events.db", "dir/with space.db", "dir/with#hash.db", "dir/with%percent.db"} {
			got := sqliteDSN(path)
			want := path + "?" + pragmaDSNParams
			if got != want {
				t.Fatalf("sqliteDSN(%q) = %q, want %q", path, got, want)
			}
		}
	})
	t.Run("question mark path becomes a file URI", func(t *testing.T) {
		for _, path := range []string{"ledger?part.db", "/tmp/ctx?name.db", "a?b?c.db", "?leading.db"} {
			dsn := sqliteDSN(path)
			if !strings.HasPrefix(dsn, "file:") {
				t.Fatalf("sqliteDSN(%q) = %q, want a file: URI", path, dsn)
			}
			rest := strings.TrimPrefix(dsn, "file:")
			sep := strings.IndexByte(rest, '?')
			if sep < 0 {
				t.Fatalf("sqliteDSN(%q) = %q has no query separator", path, dsn)
			}
			encoded := rest[:sep]
			if strings.Contains(encoded, "?") {
				t.Fatalf("path portion of sqliteDSN(%q) = %q still contains a literal '?'", path, encoded)
			}
			decoded, err := url.PathUnescape(encoded)
			if err != nil {
				t.Fatalf("PathUnescape(%q): %v", encoded, err)
			}
			if decoded != path {
				t.Fatalf("PathUnescape(%q) = %q, want %q", encoded, decoded, path)
			}
			query := rest[sep+1:]
			if query != pragmaDSNParams {
				t.Fatalf("query of sqliteDSN(%q) = %q, want %q", path, query, pragmaDSNParams)
			}
		}
	})
}
