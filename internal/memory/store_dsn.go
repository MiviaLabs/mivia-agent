package memory

import (
	"net/url"
	"strings"
)

// pragmaMemoryDSNParams are the driver-side pragma overrides applied by
// modernc.org/sqlite's applyQueryParams to every pooled connection, matching
// the storage package's pragmaDSNParams (without foreign_keys, which this
// schema does not use). Keep the two in sync when the set changes.
const pragmaMemoryDSNParams = "_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)"

// pragmaMemoryDSNReadOnlyParams are the read-only overrides: no
// journal_mode(WAL) (switching would write the database header), and
// query_only(1) so no pooled connection can mutate the committed file.
const pragmaMemoryDSNReadOnlyParams = "_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=query_only(1)"

// sqliteMemoryDSN builds the read-write driver DSN; see memoryDSN.
func sqliteMemoryDSN(path string) string { return memoryDSN(path, pragmaMemoryDSNParams) }

// sqliteMemoryDSNReadOnly builds the read-only driver DSN; see memoryDSN.
func sqliteMemoryDSNReadOnly(path string) string {
	return memoryDSN(path, pragmaMemoryDSNReadOnlyParams)
}

// memoryDSN builds the driver DSN for a memory store file. The
// modernc.org/sqlite driver (v1.54.0) splits a DSN at the first literal '?' to
// separate the filename from its query parameters, so a POSIX filename
// containing '?' ("ctx?name.db") would be silently truncated to "ctx" and the
// store would open the wrong database file. Paths without '?' keep the
// historical path+"?"+pragmas form byte-for-byte; paths with '?' are
// percent-escaped into a file: URI whose only literal '?' is the query
// separator, and SQLite's URI decoder (SQLITE_OPEN_URI) restores the literal
// '?' (and '/' for absolute paths) in the real filename. Escaping the whole
// path also prevents URI authority ("//"), query ('?'), and fragment ('#')
// injection into the path portion, so no wrong-file or path-confusion vector
// remains. Keep this in sync with internal/storage/sqlite_dsn.go; the storage
// package applies the identical transform.
func memoryDSN(path, params string) string {
	if strings.Contains(path, "?") {
		return "file:" + url.PathEscape(path) + "?" + params
	}
	return path + "?" + params
}
