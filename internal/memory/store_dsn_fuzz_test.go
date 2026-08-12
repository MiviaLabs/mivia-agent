package memory

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzSQLiteMemoryDSN asserts the sqliteMemoryDSN contract for every input
// path, mirroring the storage package's FuzzSQLiteDSN: a path without '?'
// keeps the plain path+"?"+pragmas DSN byte-for-byte; a path with '?' becomes
// a file: URI whose path portion contains no literal '?' and whose
// percent-decoded path portion round-trips to the input path. The invariant
// matters because the modernc.org/sqlite driver splits a DSN at the first
// literal '?', so any literal '?' inside the path portion would truncate the
// filename and open the wrong database file. sqliteMemoryDSN is a pure string
// function over a closed input space, so this deterministic target is the
// bounded host fuzz gate for the fix.
func FuzzSQLiteMemoryDSN(f *testing.F) {
	for _, seed := range []string{
		"events.db",           // plain
		"ledger?part.db",      // single '?'
		"a?b?c.db",            // double '?'
		"?leading.db",         // leading '?'
		"/tmp/ctx?name.db",    // absolute path with '?'
		"dir/with%percent.db", // '%' in path
		"dir/with#hash.db",    // '#' in path
		"dir/with space.db",   // space in path
		"dir/with;semi.db",    // ';' in path
		"dir/with,comma.db",   // ',' in path
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, path string) {
		dsn := sqliteMemoryDSN(path)
		if !strings.Contains(path, "?") {
			want := path + "?" + pragmaMemoryDSNParams
			if dsn != want {
				t.Fatalf("sqliteMemoryDSN(%q) = %q, want %q", path, dsn, want)
			}
			return
		}
		if !strings.HasPrefix(dsn, "file:") {
			t.Fatalf("sqliteMemoryDSN(%q) = %q, want a file: URI", path, dsn)
		}
		rest := strings.TrimPrefix(dsn, "file:")
		sep := strings.IndexByte(rest, '?')
		if sep < 0 {
			t.Fatalf("sqliteMemoryDSN(%q) = %q has no query separator", path, dsn)
		}
		encoded := rest[:sep]
		if strings.Contains(encoded, "?") {
			t.Fatalf("path portion of sqliteMemoryDSN(%q) = %q still contains a literal '?'", path, encoded)
		}
		decoded, err := url.PathUnescape(encoded)
		if err != nil {
			t.Fatalf("PathUnescape(%q): %v", encoded, err)
		}
		if decoded != path {
			t.Fatalf("PathUnescape(%q) = %q, want %q", encoded, decoded, path)
		}
	})
}
